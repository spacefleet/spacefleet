// Package tofu renders the shell script an OpenTofu (Terraform) workflow
// component runs: clone a git repo at a ref, cd into the root module, configure
// the state backend (a generated backend_override.tf — the backend is always
// managed by Spacefleet, so state lands where the component config says
// regardless of any backend block the module ships with), `tofu init`, then
// `tofu plan` or `tofu apply`
// against the target. Like lib/helm and lib/manifest, Go never runs tofu itself
// — it only emits the /bin/sh script and lets lib/tekton inject the credential
// files (the git-credentials line for a private repo, the AWS env file for
// cloud auth, the kubeconfig for attached cluster auth). This package is a pure
// renderer: no I/O, no
// ent dependency, unit tested with plain string assertions, mirroring
// lib/manifest/apply.go's Script.
//
// A terraform deployment is modelled in the DAG as two nodes — a plan node
// (Command="plan", whose stdout is the review material) and an apply node
// (Command="apply") that depends on the plan node and is gated by
// requires_approval. The plan node's `-out` planfile cannot survive into the
// apply node's pod via the filesystem (separate TaskRuns, separate ephemeral
// workspaces), so the planfile is handed over through a Kubernetes Secret: the
// plan node writes `tofu plan -out=tfplan` and stores tfplan in a Secret
// (PlanArtifactSecret) in the run namespace; the apply node fetches that Secret
// and runs `tofu apply tfplan`, applying the EXACT reviewed plan rather than
// re-planning. The worker pre-creates that Secret (empty) together with a
// per-pair ServiceAccount/Role/RoleBinding pinned to exactly it (see
// lib/tekton's EnsureHandoverSecret), and both pods run as that
// ServiceAccount: the in-pod kubectl below therefore needs only the
// name-pinnable get/patch/delete verbs — never `create` — so the user-supplied
// module code running in these pods can touch nothing else in the namespace.
// Because a saved plan is bound to the state lineage/serial it was planned
// against, the plan and apply nodes must share the same backend state identity
// (the planner keys both off the plan node's id); if state drifted between plan
// and apply, `tofu apply tfplan` fails loudly (stale plan) instead of silently
// applying something different. kubectl is not in the base image, so the
// store/fetch paths `apk add` it first (the script already does network I/O
// for the clone and provider downloads).
package tofu

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spacefleet/spacefleet/lib/tekton"
)

// The CLI image the step runs in is per-version — see versions.go (the
// Version registry); the planner resolves the component's tofu_version to a
// pinned image there.

// File names injected into the terraform step (keys in tekton.RunSpec.Files),
// mounted under tekton.CredsMountPath. They match the helm/manifest renderer
// names because the shared resolver (lib/deploy) assembles the same Files map: a
// git-credentials line for a private github.com clone and, when a cloud
// credential is attached, the AWS env file. A terraform component has no cluster
// target, so no kubeconfig is injected. Kept as local constants so this package
// does not import lib/helm for them (avoiding an import cycle and decoupling the
// renderers).
const (
	// GitCredentialsFile carries a git credential line
	// (https://x-access-token:<token>@github.com) for a private-Git repo, wired
	// into git via `credential.helper store --file=<this>` so the token is read
	// from the mounted file at runtime and never lands in the script string, the
	// clone's argv, the TaskRun manifest, or the workspace's .git/config.
	GitCredentialsFile = "git-credentials"
	// AWSEnvFile carries `export K='V'` lines for cloud (AWS) authentication —
	// the s3 state backend and the module's AWS providers read it from the
	// process env. When Apply.HasCloudAuth is set the script sources it
	// (`. <mount>/aws.env`) before `tofu init`, so the credential values live only
	// in the mounted file + the step's process env — never in the script string
	// or the TaskRun manifest, exactly as the git-credentials file does.
	AWSEnvFile = "aws.env"
	// KubeconfigFile carries the portable kubeconfig for the component's
	// attached cluster authentication (auth_cluster_id) — a registered cluster
	// the module's Kubernetes-backed providers (kubernetes/helm/kubectl)
	// authenticate to. Same name as helm.KubeconfigFile because the shared
	// resolver (lib/deploy) injects it under that key; kept local like the
	// constants above. When Apply.HasClusterAuth is set the script exports
	// KUBE_CONFIG_PATH — the env var those providers read; they deliberately
	// ignore KUBECONFIG — pointing at the mounted file. KUBECONFIG itself is
	// deliberately NOT exported: the planfile-handover kubectl calls below must
	// keep using the pod's own in-cluster credentials (the pinned per-pair
	// ServiceAccount), and a global KUBECONFIG would redirect them at the auth
	// cluster instead.
	KubeconfigFile = "kubeconfig"
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

// PlanfileName is the local filename the plan node saves its planfile to
// (`tofu plan -out=tfplan`) and the apply node restores it to before
// `tofu apply tfplan`. It is also the key the planfile is stored under inside
// the PlanArtifactSecret.
const PlanfileName = "tfplan"

// BackendS3 names the Amazon S3 state backend — the only supported backend
// type today (the workflow validation enforces it; more types will join this
// list). The backend is always managed by Spacefleet: the script writes a
// backend_override.tf so the root module's state lands where the component
// config says, regardless of any backend block the module ships with.
const BackendS3 = "s3"

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
	// Backend names the state backend (BackendS3 is the only supported value
	// today; the workflow validation enforces it).
	Backend string
	// BackendConfig is the decoded backend settings (e.g. the s3
	// bucket/key/region) rendered into backend_override.tf as `key = "value"`
	// lines. Values are rendered verbatim as HCL strings.
	BackendConfig map[string]string
	// Namespace is the runner-cluster namespace the planfile-handover Secret is
	// stored in (the same namespace the TaskRun runs in).
	Namespace string
	// HasGitToken authenticates a private github.com clone via the mounted
	// GitCredentialsFile + a credential helper, set by the resolver when the
	// component has a GitHub App installation attached.
	HasGitToken bool
	// HasCloudAuth, when set, sources the mounted AWSEnvFile (cloud/AWS
	// credentials as `export K='V'` lines) before `tofu init` so the state
	// backend and the module's providers authenticate from the process env. The
	// values never appear in the script string or manifest.
	HasCloudAuth bool
	// HasClusterAuth, when set, exports KUBE_CONFIG_PATH pointing at the
	// mounted KubeconfigFile (the attached cluster authentication) before
	// `tofu init`, so the module's kubernetes/helm/kubectl providers
	// authenticate to that cluster from an unconfigured provider block. See
	// the KubeconfigFile doc for why KUBECONFIG itself is not exported.
	HasClusterAuth bool
	// PlanArtifactSecret is the name of the Kubernetes Secret (in Namespace, on
	// the runner cluster) the planfile is handed over through. The worker
	// pre-creates it empty, alongside the same-named ServiceAccount the step's
	// pod runs as, whose Role is pinned to exactly this Secret; the plan node
	// stores `tofu plan -out=tfplan` into it (a get+patch upsert — the pod may
	// not create Secrets) and the apply node restores it and runs `tofu apply
	// tfplan`. Empty disables the planfile path: a plan node just plans (the
	// read-only review case, e.g. preview) and an apply node has no reviewed
	// plan to apply, so it fails closed. The planner sets it (keyed off the plan
	// node's id) for non-preview plan/apply nodes.
	PlanArtifactSecret string
	// InitFlags, PlanFlags, and ApplyFlags are optional operator-supplied CLI flag
	// tokens appended verbatim (each shell-quoted as one whole argv token) to
	// `tofu init`, `tofu plan`, and `tofu apply` respectively — after the flags
	// this renderer always sets (-no-color, -out, the -backend-config flags). Each
	// slice element is one argument (e.g. "-var=env=prod", "-target=aws_instance.web"),
	// so a flag that takes a separate value is two elements ("-var-file", "prod.tfvars")
	// or one "=" element ("-var-file=prod.tfvars").
	//
	// PlanFlags also apply to a preview (which is always a read-only plan) and to
	// an uninstall's destroy plan. Because an apply node applies the plan node's
	// SAVED planfile rather than re-planning, plan-time flags (-var/-var-file/
	// -target/-replace) belong in PlanFlags, not ApplyFlags — ApplyFlags is only
	// for flags valid against a saved plan (e.g. -parallelism). They are inert on a
	// preview (no apply runs) and on the fail-closed apply guard.
	InitFlags  []string
	PlanFlags  []string
	ApplyFlags []string
}

// Script renders the /bin/sh script the terraform step runs. It clones the root
// module, cds into the working path, writes the generated backend_override.tf
// (the named backend from Backend + BackendConfig), runs `tofu init`, then
// plans or applies per Command/Action:
//
//   - Command=plan, deploy:    tofu plan -out=tfplan -no-color, then store tfplan
//   - Command=plan, uninstall: tofu plan -destroy -out=tfplan -no-color, then store
//   - Command=apply, deploy/uninstall: restore tfplan, tofu apply tfplan (the
//     saved plan already encodes deploy-vs-destroy), then delete the Secret
//   - Action=preview (any Command): tofu plan -no-color (read-only; preview
//     never mutates and produces no planfile, so it is always a plain plan even
//     on an apply node)
//
// The plan output IS the review material — it is captured as the component_run
// logs the human reads before approving the apply node. When PlanArtifactSecret
// is set the plan node additionally hands the binary planfile to the apply node
// through that Kubernetes Secret (see the package doc), and the apply node
// applies it verbatim instead of re-planning. `set -e` makes any intermediate
// failure fail the step. The git token is never written into the script — only
// via the mounted credentials file + a credential helper, exactly as lib/helm
// and lib/manifest do.
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

	// The state backend is always explicit (validated at write time); the
	// planfile-handover Secret below is read/written with the step's own
	// in-cluster credentials — the per-pair ServiceAccount the worker
	// provisioned, whose Role pins it to exactly that Secret — since the job
	// already runs in the runner cluster.

	// With cloud auth, source the mounted AWS env file so the credential values
	// enter the process env before init — they live only in the mounted file +
	// env, never in the script string or the TaskRun manifest. `. ` is the POSIX
	// `source` builtin for /bin/sh.
	if a.HasCloudAuth {
		fmt.Fprintf(&b, ". %s\n", tekton.CredsMountPath+"/"+AWSEnvFile)
	}

	// With cluster auth, point the module's Kubernetes-backed providers at the
	// mounted kubeconfig. KUBE_CONFIG_PATH is the env var the kubernetes/helm/
	// kubectl providers read (they deliberately ignore KUBECONFIG, and an
	// explicit path also keeps a bare provider block from falling back to the
	// pod's own near-powerless ServiceAccount). KUBECONFIG is deliberately NOT
	// exported — the planfile-handover kubectl calls must stay on the pod's
	// in-cluster credentials (see the KubeconfigFile doc).
	if a.HasClusterAuth {
		fmt.Fprintf(&b, "export KUBE_CONFIG_PATH=%s\n", shQuote(tekton.CredsMountPath+"/"+KubeconfigFile))
	}

	// Generate the backend override so the root module's state lands where we
	// decide regardless of any backend block it ships with. backend_override.tf
	// is read by tofu init alongside the module's own .tf files; an *_override.tf
	// file merges over a matching block, so this wins.
	b.WriteString(backendOverride(a))
	b.WriteString("tofu init -no-color")
	appendFlags(&b, a.InitFlags)
	b.WriteString("\n")

	preview := a.Action == ActionPreview
	destroy := a.Action == ActionUninstall

	switch {
	case a.Command == CommandApply && !preview:
		// apply node: apply the EXACT planfile the plan node produced and the human
		// reviewed, restored from the handover Secret — never a fresh re-plan. The
		// saved plan already encodes deploy-vs-destroy, so there is no -destroy here.
		if a.PlanArtifactSecret == "" {
			// No reviewed plan to apply. The DAG validation makes an apply node
			// without an upstream plan node invalid, so this is a defensive guard:
			// fail closed rather than silently re-planning.
			b.WriteString("echo 'no reviewed planfile to apply (no upstream plan node)' >&2\nexit 1\n")
			return b.String()
		}
		b.WriteString(restorePlanfile(a))
		// Apply flags go before the positional planfile arg (tofu apply [options] PLAN).
		b.WriteString("tofu apply -no-color")
		appendFlags(&b, a.ApplyFlags)
		fmt.Fprintf(&b, " %s\n", PlanfileName)
		b.WriteString(deletePlanfileSecret(a))
	default:
		// plan node, or any preview: a read-only plan. The stdout is the review
		// material captured as the component_run logs. A non-preview plan node also
		// saves the planfile and hands it to its apply node through the Secret.
		if preview {
			if destroy {
				b.WriteString("tofu plan -destroy -no-color")
			} else {
				b.WriteString("tofu plan -no-color")
			}
			appendFlags(&b, a.PlanFlags)
			b.WriteString("\n")
			break
		}
		if destroy {
			fmt.Fprintf(&b, "tofu plan -destroy -out=%s -no-color", PlanfileName)
		} else {
			fmt.Fprintf(&b, "tofu plan -out=%s -no-color", PlanfileName)
		}
		appendFlags(&b, a.PlanFlags)
		b.WriteString("\n")
		if a.PlanArtifactSecret != "" {
			b.WriteString(storePlanfile(a))
		}
	}

	return b.String()
}

// kubectlInstall emits the line that makes kubectl available in the tofu step.
// The OpenTofu image is Alpine without kubectl; `apk add` pulls it from the
// already-enabled community repo. Emitted only on the planfile store/fetch
// paths (network I/O is already expected there — the clone and provider
// downloads), never for a read-only preview.
const kubectlInstall = "apk add --no-cache kubectl\n"

// storePlanfile emits the lines a non-preview plan node runs to hand its saved
// planfile to the apply node: install kubectl, then upsert the planfile into the
// PlanArtifactSecret (a client-side apply, idempotent so an approval-resume
// re-run is safe). The Secret already exists — the worker pre-created it — so
// the apply is a get+patch, the only secret verbs (plus delete) the pod's
// pinned Role grants; the `kubectl create --dry-run=client` stage only renders
// the manifest locally. No KUBECONFIG is exported, so kubectl uses the step's
// in-cluster credentials — the per-pair ServiceAccount — and the Secret lives
// in the runner cluster (the namespace the TaskRun runs in), reachable by both
// the plan and apply nodes.
func storePlanfile(a Apply) string {
	var b strings.Builder
	b.WriteString(kubectlInstall)
	fmt.Fprintf(&b, "kubectl create secret generic %s --namespace %s --from-file=%s=%s --dry-run=client -o yaml | kubectl apply --namespace %s -f -\n",
		shQuote(a.PlanArtifactSecret), shQuote(a.Namespace), PlanfileName, PlanfileName, shQuote(a.Namespace))
	return b.String()
}

// restorePlanfile emits the lines an apply node runs to fetch and decode the
// planfile its plan node stored, before `tofu apply tfplan`. The Secret stores
// the planfile base64-encoded under the PlanfileName key; jsonpath returns that
// base64 and `base64 -d` (busybox) restores the binary planfile.
func restorePlanfile(a Apply) string {
	var b strings.Builder
	b.WriteString(kubectlInstall)
	fmt.Fprintf(&b, "kubectl get secret %s --namespace %s -o %s | base64 -d > %s\n",
		shQuote(a.PlanArtifactSecret), shQuote(a.Namespace), shQuote("jsonpath={.data."+PlanfileName+"}"), PlanfileName)
	return b.String()
}

// deletePlanfileSecret emits the best-effort cleanup an apply node runs after a
// successful apply — the planfile has served its purpose, and deleting the
// Secret garbage-collects the pair's ServiceAccount/Role/RoleBinding through
// their ownerReferences. --ignore-not-found keeps it idempotent; cleanup
// failure does not fail the step (the apply already succeeded) — the worker
// sweeps any leftover when the run settles terminal.
func deletePlanfileSecret(a Apply) string {
	return fmt.Sprintf("kubectl delete secret %s --namespace %s --ignore-not-found || true\n",
		shQuote(a.PlanArtifactSecret), shQuote(a.Namespace))
}

// backendOverride renders the backend_override.tf the step writes before init.
// It emits the named backend (a.Backend, e.g. s3) with the BackendConfig
// key/values rendered as HCL strings, in sorted key order for a stable
// (testable) output. The heredoc is single-quoted ('EOF') so nothing in the
// body is shell-expanded.
func backendOverride(a Apply) string {
	var body strings.Builder
	fmt.Fprintf(&body, "terraform {\n  backend %q {\n", a.Backend)
	keys := make([]string, 0, len(a.BackendConfig))
	for k := range a.BackendConfig {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&body, "    %s = %q\n", k, a.BackendConfig[k])
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

// appendFlags writes each operator-supplied flag token to b as a space-separated,
// shell-quoted argument, so a flag value can't break out of the command line into
// the script. Each element is treated as one whole argv token (quoted as a unit);
// blank tokens are skipped so a stray empty entry doesn't emit a bare ”. The
// caller has already written the command and its fixed flags, with no trailing
// newline yet.
func appendFlags(b *strings.Builder, flags []string) {
	for _, f := range flags {
		if f == "" {
			continue
		}
		fmt.Fprintf(b, " %s", shQuote(f))
	}
}

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
