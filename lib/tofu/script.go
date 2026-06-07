// Package tofu renders the shell script an OpenTofu (Terraform) workflow
// component runs: clone a git repo at a ref, cd into the root module, configure
// the state backend (a generated backend_override.tf in managed mode, or the
// module's own backend block plus `-backend-config` flags in byo mode),
// `tofu init`, then `tofu plan` or `tofu apply`
// against the target. Like lib/helm and lib/manifest, Go never runs tofu itself
// — it only emits the /bin/sh script and lets lib/tekton inject the credential
// files (the target kubeconfig the Kubernetes backend uses, and, for a private
// repo, the git-credentials line). This package is a pure renderer: no I/O, no
// ent dependency, unit tested with plain string assertions, mirroring
// lib/manifest/apply.go's Script.
//
// A terraform deployment is modelled in the DAG as two nodes — a plan node
// (Command="plan", whose output is the review material) and an apply node
// (Command="apply") that depends on the plan node and is gated by
// requires_approval. The plan node's `-out` plan artifact does NOT survive into
// the apply node's pod (separate TaskRuns, separate workspaces), so the apply
// node re-plans implicitly: `tofu apply -auto-approve` plans-then-applies
// against the SHARED backend state. This re-plan-on-apply is the accepted v1
// behavior — between the human's approval of the plan node's output and the
// apply node running, drift is possible, but the shared state keeps the apply
// consistent with reality. Pinning the exact reviewed plan is a documented
// follow-up.
package tofu

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spacefleet/spacefleet/lib/tekton"
)

// DefaultImage is the OpenTofu CLI image the terraform step runs in.
//
// We pin a pre-1.10 OpenTofu *default* image tag because it is built FROM
// alpine and runs `apk add --no-cache git bash openssh`, so it bundles git —
// which the step needs to clone the root module — alongside the tofu binary.
//
// Bundling caveat: starting with OpenTofu 1.10 the project only publishes the
// `-minimal` image (just the tofu binary, no git/bash) and no longer supports
// using its image as a base. So this pin cannot simply float forward to the
// latest tag without losing git. Upgrading past 1.9.x therefore requires
// publishing a small first-party runner image (multi-stage: copy the tofu
// binary from `ghcr.io/opentofu/opentofu:<ver>-minimal` onto an alpine base
// that `apk add git`), the same wrapper-image follow-up noted for the
// helm-diff plugin. The const is the single place to change when that lands.
const DefaultImage = "ghcr.io/opentofu/opentofu:1.9.1"

// File names injected into the terraform step (keys in tekton.RunSpec.Files),
// mounted under tekton.CredsMountPath. They match the helm/manifest renderer
// names because the shared resolver (lib/deploy) assembles the same Files map:
// the target kubeconfig always (the Kubernetes backend authenticates with it),
// and a git-credentials line for a private github.com clone. Kept as local
// constants so this package does not import lib/helm for them (avoiding an
// import cycle and decoupling the renderers).
const (
	// KubeconfigFile is the injected kubeconfig for the target cluster. The
	// Kubernetes state backend reads it via the KUBECONFIG env var so state
	// Secrets land in the cluster we already manage.
	KubeconfigFile = "kubeconfig"
	// GitCredentialsFile carries a git credential line
	// (https://x-access-token:<token>@github.com) for a private-Git repo, wired
	// into git via `credential.helper store --file=<this>` so the token is read
	// from the mounted file at runtime and never lands in the script string, the
	// clone's argv, the TaskRun manifest, or the workspace's .git/config.
	GitCredentialsFile = "git-credentials"
	// AWSEnvFile carries `export K='V'` lines for cloud (AWS) authentication in
	// the byo backend mode. When Apply.HasCloudAuth is set the script sources it
	// (`. <mount>/aws.env`) before `tofu init`, so the credential values live only
	// in the mounted file + the step's process env — never in the script string
	// or the TaskRun manifest, exactly as the kubeconfig/git-credentials files do.
	AWSEnvFile = "aws.env"
)

// Commands a terraform component runs. plan produces the review material
// (captured as the component_run logs); apply mutates infrastructure. Mirror of
// the workflow dag.go command config values.
const (
	CommandPlan  = "plan"
	CommandApply = "apply"
)

// Actions a terraform run maps to (from the workflow run action). deploy =
// create/update; uninstall = destroy; preview = a read-only plan regardless of
// the node's command. These mirror the workflow run actions the planner maps
// from.
const (
	ActionDeploy    = "deploy"
	ActionUninstall = "uninstall"
	ActionPreview   = "preview"
)

// DefaultBackend is the OpenTofu state backend used when none is configured:
// the Kubernetes backend, storing state as Secrets in the runner cluster using
// the injected kubeconfig — zero extra config.
const DefaultBackend = "kubernetes"

// Backend modes select how state backend configuration is wired. Managed (the
// default, also when empty) generates a backend_override.tf so state lands where
// we decide. BYO ("bring your own") uses the module's OWN backend block
// untouched — no override is written; any partial-backend values are passed as
// `tofu init -backend-config=k=v` flags and cloud auth is sourced from the
// mounted AWSEnvFile.
const (
	ModeManaged = "managed"
	ModeBYO     = "byo"
)

// Apply is the inputs Script needs to render the terraform shell script.
type Apply struct {
	// Command is the tofu verb the node runs: CommandPlan or CommandApply.
	Command string
	// Action is the run action: ActionDeploy / ActionUninstall / ActionPreview.
	// It selects plan-vs-destroy / apply-vs-destroy and forces a plan for preview.
	Action string
	// RepoURL is the git repository to clone the root module from.
	RepoURL string
	// GitRef is an optional branch/tag to clone (default branch when empty).
	GitRef string
	// Path is the working directory within the repo holding the root module.
	Path string
	// Backend names the state backend; empty means DefaultBackend (kubernetes).
	Backend string
	// BackendConfig is the decoded backend settings rendered into
	// backend_override.tf as `key = "value"` lines. Used for a custom backend
	// (S3/GCS/pg/etc.); ignored for the kubernetes default (which derives its
	// settings from SecretSuffix + Namespace). Values are rendered verbatim as
	// HCL strings.
	BackendConfig map[string]string
	// SecretSuffix disambiguates the Kubernetes-backend state Secret per
	// app+component (the backend stores state in a Secret named
	// tfstate-default-<secret_suffix>). The planner derives it from the
	// application + component so two components don't clobber each other's state.
	SecretSuffix string
	// Namespace is the runner-cluster namespace the Kubernetes backend stores its
	// state Secret in (the same namespace the TaskRun runs in).
	Namespace string
	// HasGitToken authenticates a private github.com clone via the mounted
	// GitCredentialsFile + a credential helper, set by the resolver when the
	// component has a GitHub App installation attached.
	HasGitToken bool
	// BackendMode selects how the state backend is wired: "" or ModeManaged
	// generate a backend_override.tf (the current behavior); ModeBYO uses the
	// module's own backend block, writing no override and passing BackendConfig
	// entries as `tofu init -backend-config=k=v` flags.
	BackendMode string
	// HasCloudAuth, when set, sources the mounted AWSEnvFile (cloud/AWS
	// credentials as `export K='V'` lines) before `tofu init` so the byo backend
	// and providers authenticate from the process env. The values never appear in
	// the script string or manifest.
	HasCloudAuth bool
}

// Script renders the /bin/sh script the terraform step runs. It clones the root
// module, cds into the working path, writes a generated backend_override.tf
// (the Kubernetes backend by default, or a custom backend from Backend +
// BackendConfig), runs `tofu init`, then plans or applies per Command/Action:
//
//   - Command=plan, deploy:    tofu plan -no-color
//   - Command=plan, uninstall: tofu plan -destroy -no-color
//   - Command=apply, deploy:   tofu apply -auto-approve -no-color
//   - Command=apply, uninstall: tofu destroy -auto-approve -no-color
//   - Action=preview (any Command): tofu plan -no-color (read-only; preview
//     never mutates, so it is always a plan even on an apply node)
//
// The plan output IS the review material — it is captured as the component_run
// logs the human reads before approving the apply node. `set -e` makes any
// intermediate failure fail the step. The git token is never written into the
// script — only via the mounted credentials file + a credential helper, exactly
// as lib/helm and lib/manifest do.
//
// Re-plan-on-apply caveat: see the package doc. The apply node re-plans (the
// plan node's artifact does not cross pods); the shared backend state keeps it
// consistent.
func Script(a Apply) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\nset -e\n")

	// Containment guard: a `..` segment could escape the clone dir and read a file
	// elsewhere in the step (e.g. the mounted creds). shQuote blocks shell
	// injection but not traversal, so reject it outright before emitting any clone.
	if hasTraversal(a.Path) {
		fmt.Fprintf(&b, "echo 'invalid path (path traversal not allowed): %s' >&2\nexit 1\n", a.Path)
		return b.String()
	}

	// Wire git's credential helper once, before the clone, so a private github.com
	// clone reads the token from the mounted file at runtime — never the script
	// string, the clone's argv, or the workspace .git/config.
	if a.HasGitToken {
		gitCredsFile := tekton.CredsMountPath + "/" + GitCredentialsFile
		fmt.Fprintf(&b, "git config --global credential.helper %s\n",
			shQuote("store --file="+gitCredsFile))
	}

	// Clone the root module. --depth 1 keeps it shallow; an explicit ref pins the
	// branch/tag.
	if a.GitRef != "" {
		fmt.Fprintf(&b, "git clone --depth 1 --branch %s %s /src\n", shQuote(a.GitRef), shQuote(a.RepoURL))
	} else {
		fmt.Fprintf(&b, "git clone --depth 1 %s /src\n", shQuote(a.RepoURL))
	}
	// Echo the resolved SHA so the worker records what this run ran against (a
	// branch can move between runs). Reuses the helm revision marker so the
	// worker's existing helm.ParseRevisions captures it as the component revision.
	fmt.Fprintf(&b, "echo \"%s$(git -C /src rev-parse HEAD)\"\n", revChartPrefix)

	fmt.Fprintf(&b, "cd %s\n", shQuote("/src/"+a.Path))

	// The Kubernetes backend authenticates with the injected target kubeconfig.
	// Export KUBECONFIG so `tofu init` finds it without a config_path in HCL
	// (which would put the path in the rendered override). Harmless for a custom
	// backend (tofu ignores KUBECONFIG when the backend isn't kubernetes).
	kubeconfig := tekton.CredsMountPath + "/" + KubeconfigFile
	fmt.Fprintf(&b, "export KUBECONFIG=%s\n", shQuote(kubeconfig))

	// In byo mode with cloud auth, source the mounted AWS env file so the
	// credential values enter the process env before init — they live only in the
	// mounted file + env, never in the script string or the TaskRun manifest. `. `
	// is the POSIX `source` builtin for /bin/sh.
	if a.HasCloudAuth {
		fmt.Fprintf(&b, ". %s\n", tekton.CredsMountPath+"/"+AWSEnvFile)
	}

	if a.BackendMode == ModeBYO {
		// BYO mode: use the module's own backend block — write no override. Pass
		// any partial-backend values as `-backend-config=k=v` flags in sorted key
		// order (stable output), each whole token shell-quoted, matching how a real
		// partial backend is filled at init time.
		b.WriteString("tofu init -no-color")
		keys := make([]string, 0, len(a.BackendConfig))
		for k := range a.BackendConfig {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, " -backend-config=%s", shQuote(k+"="+a.BackendConfig[k]))
		}
		b.WriteString("\n")
	} else {
		// Managed mode: generate the backend override so the root module's state
		// lands where we decide regardless of any backend block it ships with.
		// backend_override.tf is read by tofu init alongside the module's own .tf
		// files; an *_override.tf file merges over a matching block, so this wins.
		b.WriteString(backendOverride(a))
		b.WriteString("tofu init -no-color\n")
	}

	preview := a.Action == ActionPreview
	destroy := a.Action == ActionUninstall

	switch {
	case a.Command == CommandApply && !preview:
		// apply node: re-plan-and-apply (the reviewed plan artifact does not cross
		// pods; shared state keeps it consistent — see the package doc caveat).
		if destroy {
			b.WriteString("tofu destroy -auto-approve -no-color\n")
		} else {
			b.WriteString("tofu apply -auto-approve -no-color\n")
		}
	default:
		// plan node, or any preview: a read-only plan. The output is the review
		// material captured as the component_run logs.
		if destroy {
			b.WriteString("tofu plan -destroy -no-color\n")
		} else {
			b.WriteString("tofu plan -no-color\n")
		}
	}

	return b.String()
}

// backendOverride renders the backend_override.tf the step writes before init.
// For the kubernetes default it emits a Kubernetes backend keyed by the
// per-component secret_suffix in the runner namespace (in_cluster_config so the
// step authenticates via the mounted kubeconfig/KUBECONFIG). For any other
// backend it emits the named backend with the BackendConfig key/values rendered
// as HCL strings. The heredoc is single-quoted ('EOF') so nothing in the body
// is shell-expanded.
func backendOverride(a Apply) string {
	backend := a.Backend
	if backend == "" {
		backend = DefaultBackend
	}

	var body strings.Builder
	fmt.Fprintf(&body, "terraform {\n  backend %q {\n", backend)
	if backend == DefaultBackend {
		// Kubernetes backend: state stored as a Secret in the runner cluster. The
		// secret_suffix is per app+component so components don't clobber each
		// other; in_cluster_config=false makes it read the mounted KUBECONFIG.
		fmt.Fprintf(&body, "    secret_suffix    = %q\n", a.SecretSuffix)
		if a.Namespace != "" {
			fmt.Fprintf(&body, "    namespace        = %q\n", a.Namespace)
		}
		body.WriteString("    in_cluster_config = false\n")
	} else {
		// Custom backend: render the operator-provided settings verbatim as HCL
		// strings, in sorted key order for a stable (testable) output.
		keys := make([]string, 0, len(a.BackendConfig))
		for k := range a.BackendConfig {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&body, "    %s = %q\n", k, a.BackendConfig[k])
		}
	}
	body.WriteString("  }\n}\n")

	var b strings.Builder
	b.WriteString("cat > backend_override.tf <<'EOF'\n")
	b.WriteString(body.String())
	b.WriteString("EOF\n")
	return b.String()
}

// revChartPrefix is the resolved-commit log marker the script echoes after the
// clone — intentionally the SAME string lib/helm/lib/manifest use so the
// worker's existing helm.ParseRevisions captures a terraform component's
// resolved SHA into chart_revision unchanged (no worker change). If lib/helm's
// marker ever changes, update this in lockstep.
const revChartPrefix = "SPACEFLEET_CHART_REVISION="

// shQuote single-quotes a value for safe interpolation into the /bin/sh script,
// escaping any embedded single quotes. Replicated from lib/helm/lib/manifest
// (where it is unexported) so this renderer stays self-contained.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hasTraversal reports whether p contains a ".." path segment, so the working
// path can't escape the clone directory. It checks segments (not a substring)
// so a legitimate name like "..foo" is allowed; only a bare ".." component is
// rejected. Backslashes are normalized to forward slashes first. Replicated
// from lib/manifest (where it is unexported).
func hasTraversal(p string) bool {
	p = strings.ReplaceAll(p, "\\", "/")
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}
