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
// plan node writes `tofu plan -input=false -out=tfplan` and stores tfplan in a Secret
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
	"bytes"
	"encoding/json"
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
// credential is attached, the cloud env file. A terraform component has no cluster
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
	// CloudEnvFile carries `export K='V'` lines for cloud authentication — the
	// provider's secret material in the environment form its state backend
	// and the module's providers read (AWS keys, the GCP service-account JSON
	// as GOOGLE_CREDENTIALS, the Azure client secret; see cloudauth.Env). When
	// Apply.HasCloudAuth is set the script sources it (`. <mount>/cloud.env`)
	// before `tofu init`, so the credential values live only in the mounted
	// file + the step's process env — never in the script string or the
	// TaskRun manifest, exactly as the git-credentials file does.
	CloudEnvFile = "cloud.env"
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
	// CommandStateOp runs one guarded state operation (Apply.StateOp) —
	// force-unlock / state rm / state mv / import — instead of a plan or
	// apply. The unit is the whole run: it initialises the backend, runs the
	// operation, then hands the refreshed outputs + resource inventory back
	// through the handover Secret exactly as a deploy apply does, so the
	// component's recorded state stays current.
	CommandStateOp = "state_op"
)

// Actions a terraform run maps to (from the workflow run action). deploy =
// create/update; uninstall = destroy; preview = a read-only plan regardless of
// the node's command. These mirror the workflow run actions the planner maps
// from.
const (
	ActionDeploy    = "deploy"
	ActionUninstall = "uninstall"
	ActionPreview   = "preview"
	// ActionDrift is a drift check: a read-only refresh-only plan
	// (`tofu plan -refresh-only`) that reports what changed outside of OpenTofu
	// since the last apply, without proposing configuration changes.
	ActionDrift = "drift"
	// ActionStateOp is a guarded state operation run (CommandStateOp): it
	// mutates state (or its lock), never infrastructure, and is always gated.
	ActionStateOp = "state_op"
)

// TFVarsFile is the variable-definitions file the script writes into the
// root module from Apply.TFVars before any command runs. The `.auto.tfvars
// .json` suffix makes OpenTofu load it automatically for plan, apply
// (-refresh-only and -destroy included) and import, after the environment's
// TF_VAR_ values and before any explicit -var / -var-file flag, so a plan
// flag still overrides a typed input.
const TFVarsFile = "spacefleet.auto.tfvars.json"

// PlanfileName is the local filename the plan node saves its planfile to
// (`tofu plan -input=false -out=tfplan`) and the apply node restores it to before
// `tofu apply tfplan`. It is also the key the planfile is stored under inside
// the PlanArtifactSecret.
const PlanfileName = "tfplan"

// OutputsFile is the local filename an apply node saves `tofu output -json`
// to after a successful apply (a destroy included — it records the emptied
// outputs). The JSON is never echoed to
// stdout: `-json` does not redact sensitive output values, and the step logs
// are persisted and live-streamed — the outputs travel back to the worker only
// through the handover Secret (see OutputsKey).
const OutputsFile = "sf-outputs.json"

// ResourcesFile is the local filename an apply node saves the managed
// resource inventory to after a successful apply (after a destroy: whatever
// is left, nothing after a full one): `tofu show -json` reduced in
// the pod (with jq) to one small record per resource — address, mode, type,
// name, provider, id — so the full state, which carries every attribute value
// including sensitive ones, never leaves the pod and the record stays well
// under the handover Secret's size limit.
const ResourcesFile = "sf-resources.json"

// ResourcesKey is the key the resource inventory is upserted under inside the
// PlanArtifactSecret, alongside OutputsKey.
const ResourcesKey = "resources"

// resourcesFilter is the jq program that reduces `tofu show -json` to the
// inventory: every resource of the root module and, recursively, its child
// modules. Resource addresses in the JSON are already module-qualified.
// `.values.id` is the provider-assigned identifier most resources carry; a
// resource without one records null. The program contains no single quotes
// so it can be passed as one single-quoted shell argument.
const resourcesFilter = `[.values.root_module | recurse(.child_modules[]?) | .resources[]? | {address, mode, type, name, provider: .provider_name, id: ((.values // {}).id // null)}]`

// OutputsKey is the key the captured outputs JSON is upserted under inside the
// PlanArtifactSecret after a successful apply — the planfile in there is
// spent by then, so the Secret's last job is carrying the outputs back. The
// worker reads this key when the apply node settles succeeded, persists it on
// the component_run row, then deletes the Secret; that is why the apply path
// has no in-script delete. A destroy apply records too: its emptied outputs
// and remaining inventory are what the component now owns.
const OutputsKey = "outputs"

// The supported state backends (the workflow validation enforces the set and
// each one's required settings). The backend is always managed by Spacefleet:
// the script writes a backend_override.tf so the root module's state lands
// where the component config says, regardless of any backend block the module
// ships with. Authentication comes from the attached cloud credential of the
// matching provider (or the runner's own identity when none is attached).
const (
	// BackendS3 is Amazon S3 (bucket / key / region; optional dynamodb_table,
	// encrypt, kms_key_id, use_lockfile).
	BackendS3 = "s3"
	// BackendGCS is Google Cloud Storage (bucket / prefix). Locking is native.
	BackendGCS = "gcs"
	// BackendAzure is Azure Blob Storage — OpenTofu's azurerm backend
	// (storage_account_name / container_name / key; optional resource_group_name).
	// Locking is native (blob leases).
	BackendAzure = "azurerm"
	// BackendSpacefleet is Spacefleet's managed state: the script renders
	// OpenTofu's `http` backend pointed at this Spacefleet (Apply.StateAddress),
	// which keeps the state versioned and sealed in its own database. It has
	// no settings of its own, and its credentials — a per-step token — come
	// from TF_HTTP_USERNAME / TF_HTTP_PASSWORD in the step's environment, so
	// they never appear in the script or the generated backend file.
	BackendSpacefleet = "spacefleet"
)

// The `http` backend's lock and unlock methods for managed state. POST and
// DELETE rather than the backend's default LOCK / UNLOCK verbs, which some
// ingress controllers, WAFs, and proxies drop.
const (
	stateLockMethod   = "POST"
	stateUnlockMethod = "DELETE"
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
	// Backend names the state backend (one of the Backend* constants; the
	// workflow validation enforces it).
	Backend string
	// BackendConfig is the decoded backend settings (e.g. the s3
	// bucket/key/region) rendered into backend_override.tf as `key = "value"`
	// lines. Values are rendered verbatim as HCL strings.
	BackendConfig map[string]string
	// StateAddress is the managed state URL for BackendSpacefleet (the
	// planner builds it from the URL runner pods reach Spacefleet at); its
	// lock address is StateAddress + "/lock". Ignored for every other
	// backend. Empty with BackendSpacefleet fails the step closed.
	StateAddress string
	// Namespace is the runner-cluster namespace the planfile-handover Secret is
	// stored in (the same namespace the TaskRun runs in).
	Namespace string
	// HasGitToken authenticates a private github.com clone via the mounted
	// GitCredentialsFile + a credential helper, set by the resolver when the
	// component has a GitHub App installation attached.
	HasGitToken bool
	// HasCloudAuth, when set, sources the mounted CloudEnvFile (the cloud
	// credential's secret material as `export K='V'` lines) before `tofu init`
	// so the state backend and the module's providers authenticate from the
	// process env. The values never appear in the script string or manifest.
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
	// stores `tofu plan -input=false -out=tfplan` into it (a get+patch upsert — the pod may
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
	// StateOp is the operation a CommandStateOp unit runs. Ignored for every
	// other command; a state_op unit without one fails closed.
	StateOp *StateOp
	// Workspace, when set, is the OpenTofu workspace every command runs in:
	// the script selects it right after `tofu init` (creating it on first
	// use), so plan, apply, drift, and state operations all address that
	// workspace's state. Empty keeps the default workspace. The workflow
	// validation restricts the name to a safe token. BackendSpacefleet
	// ignores it here: the `http` backend has no workspaces, so the planner
	// puts the workspace into StateAddress instead.
	Workspace string
	// TFVars, when set, is a JSON object of typed root-module input variables
	// (name → any JSON value) written into the module as TFVarsFile before
	// init — so every command of the component (plan, apply, drift, import)
	// sees the same inputs. It is rendered into the script verbatim (compacted
	// to one line), so it must never carry secrets: those go through the
	// mounted env (TF_VAR_ from the resolver). Validated as an object at
	// write time by the workflow validation.
	TFVars string
	// PluginCacheDir, when set, is exported as TF_PLUGIN_CACHE_DIR before
	// `tofu init`: the mounted per-runner-cluster provider plugin cache (see
	// tekton.PluginCacheMountPath), so providers are downloaded once per
	// cluster. Empty leaves OpenTofu downloading into the workspace.
	PluginCacheDir string
}

// Script renders the /bin/sh script the terraform step runs. It clones the root
// module, cds into the working path, writes the generated backend_override.tf
// (the named backend from Backend + BackendConfig), runs `tofu init`, then
// plans or applies per Command/Action:
//
//   - Command=plan, deploy:    tofu plan -input=false -out=tfplan -no-color, then store tfplan
//   - Command=plan, uninstall: tofu plan -input=false -destroy -out=tfplan -no-color, then store
//   - Command=apply, deploy/uninstall: restore tfplan, tofu apply tfplan (the
//     saved plan already encodes deploy-vs-destroy), then hand the outputs +
//     inventory back through the Secret
//   - Action=preview (any Command): tofu plan -input=false -no-color (read-only; preview
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

	// Typed inputs: one auto-loaded tfvars file, written before anything else
	// so plan, apply, drift, and import all read the same values. The heredoc
	// is single-quoted ('EOF') so nothing in the body is shell-expanded; the
	// body is compact JSON — one line that cannot itself read "EOF".
	if tfvars := strings.TrimSpace(a.TFVars); tfvars != "" {
		b.WriteString(tfvarsFile(tfvars))
	}

	// The state backend is always explicit (validated at write time); the
	// planfile-handover Secret below is read/written with the step's own
	// in-cluster credentials — the per-pair ServiceAccount the worker
	// provisioned, whose Role pins it to exactly that Secret — since the job
	// already runs in the runner cluster.

	// With cloud auth, source the mounted cloud env file so the credential values
	// enter the process env before init — they live only in the mounted file +
	// env, never in the script string or the TaskRun manifest. `. ` is the POSIX
	// `source` builtin for /bin/sh.
	if a.HasCloudAuth {
		fmt.Fprintf(&b, ". %s\n", tekton.CredsMountPath+"/"+CloudEnvFile)
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

	// Provider plugin cache: point init at the mounted per-cluster cache so a
	// provider already there is linked instead of downloaded again.
	if a.PluginCacheDir != "" {
		fmt.Fprintf(&b, "export TF_PLUGIN_CACHE_DIR=%s\n", shQuote(a.PluginCacheDir))
	}

	// Managed state needs the address the planner built; without one there
	// is nowhere to keep state, so fail before init rather than let OpenTofu
	// fall back to anything.
	if a.Backend == BackendSpacefleet && a.StateAddress == "" {
		b.WriteString("echo 'no managed state address for the spacefleet backend' >&2\nexit 1\n")
		return b.String()
	}

	// Generate the backend override so the root module's state lands where we
	// decide regardless of any backend block it ships with. backend_override.tf
	// is read by tofu init alongside the module's own .tf files; an *_override.tf
	// file merges over a matching block, so this wins.
	b.WriteString(backendOverride(a))
	b.WriteString("tofu init -input=false -no-color")
	appendFlags(&b, a.InitFlags)
	b.WriteString("\n")

	// Workspace: select (or create on first use) before any command touches
	// state, so every unit of the component — plan and apply, a drift check, a
	// state operation — addresses the same workspace. The s3 backend keys a
	// non-default workspace's state under env:/<workspace>/<key>. Managed
	// state has no OpenTofu workspaces (the `http` backend doesn't support
	// them): the workspace is part of its address instead.
	if a.Workspace != "" && a.Backend != BackendSpacefleet {
		fmt.Fprintf(&b, "tofu workspace select -or-create=true %s\n", shQuote(a.Workspace))
	}

	preview := a.Action == ActionPreview || a.Action == ActionDrift
	drift := a.Action == ActionDrift
	destroy := a.Action == ActionUninstall

	switch {
	case a.Command == CommandStateOp:
		// A guarded state operation: the exact command the approver saw
		// (StateOp.Command renders the same argv). Every field is shell-quoted
		// as one token. The handover tooling is installed *before* the
		// operation so a tooling failure fails the step before state is
		// touched; afterwards the refreshed outputs + inventory go back
		// through the handover Secret (best-effort, like a deploy apply) so
		// the component's recorded state reflects the operation.
		if a.StateOp == nil || a.StateOp.Validate() != nil {
			b.WriteString("echo 'no valid state operation to run' >&2\nexit 1\n")
			return b.String()
		}
		if a.PlanArtifactSecret == "" {
			b.WriteString("echo 'no handover secret for the state operation' >&2\nexit 1\n")
			return b.String()
		}
		b.WriteString(applyToolsInstall)
		argv := a.StateOp.argv(ImportFlags(a.PlanFlags))
		for i, t := range argv {
			if i > 0 {
				b.WriteString(" ")
			}
			if i == 0 {
				b.WriteString(t) // the bare `tofu` binary
				continue
			}
			b.WriteString(shQuote(t))
		}
		b.WriteString("\n")
		b.WriteString(storeOutputs(a))
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
		b.WriteString("tofu apply -input=false -no-color")
		appendFlags(&b, a.ApplyFlags)
		fmt.Fprintf(&b, " %s\n", PlanfileName)
		// Deploy or destroy, the module's outputs and inventory go back through
		// the same Secret (a destroy records what is left — nothing, after a
		// full one — so the component's recorded state stops showing resources
		// that no longer exist); the worker deletes the Secret after reading
		// them, so no in-script delete here.
		b.WriteString(storeOutputs(a))
	default:
		// plan node, or any preview: a read-only plan. The stdout is the review
		// material captured as the component_run logs. A non-preview plan node also
		// saves the planfile and hands it to its apply node through the Secret.
		if preview {
			switch {
			case drift:
				// A refresh-only plan proposes no configuration changes; its only
				// content is the "changes made outside of OpenTofu" it detected.
				b.WriteString("tofu plan -input=false -refresh-only -no-color")
			case destroy:
				b.WriteString("tofu plan -input=false -destroy -no-color")
			default:
				b.WriteString("tofu plan -input=false -no-color")
			}
			appendFlags(&b, a.PlanFlags)
			b.WriteString("\n")
			break
		}
		if destroy {
			fmt.Fprintf(&b, "tofu plan -input=false -destroy -out=%s -no-color", PlanfileName)
		} else {
			fmt.Fprintf(&b, "tofu plan -input=false -out=%s -no-color", PlanfileName)
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

// applyToolsInstall is kubectlInstall plus jq, for the apply path: jq reduces
// the state to the resource inventory after a successful apply (see
// resourcesFilter).
const applyToolsInstall = "apk add --no-cache kubectl jq\n"

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
	b.WriteString(applyToolsInstall)
	fmt.Fprintf(&b, "kubectl get secret %s --namespace %s -o %s | base64 -d > %s\n",
		shQuote(a.PlanArtifactSecret), shQuote(a.Namespace), shQuote("jsonpath={.data."+PlanfileName+"}"), PlanfileName)
	return b.String()
}

// storeOutputs emits the lines an apply node (deploy or destroy) or a
// state-op unit runs after success to hand the module's outputs back to the
// worker: save `tofu output -json` to a local file — never to stdout, since
// `-json` does not redact sensitive output values and the step logs are
// persisted and live-streamed — then upsert it into the handover Secret under
// OutputsKey (the same idempotent get+patch upsert storePlanfile uses, covered
// by the pod's pinned Role; kubectl is already installed by the restore
// above). Every line tolerates failure (`||`): the apply already succeeded, so
// a capture hiccup must not fail the step. A failed capture leaves an EMPTY
// file (`: >`), which the worker reads as "nothing captured" and leaves the
// previous record in place — distinct from a genuinely empty `{}` / `[]`,
// which it records (a module without outputs, the state after a destroy).
// The Secret is not deleted here: the worker deletes it after reading the
// outputs, and the terminal sweep remains the backstop.
func storeOutputs(a Apply) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tofu output -json > %s || : > %s\n", OutputsFile, OutputsFile)
	// The resource inventory: the state reduced in-pod to one record per
	// resource (never the full state, which carries every attribute value).
	// Like the outputs it goes to a file, never stdout, and tolerates failure.
	fmt.Fprintf(&b, "tofu show -json | jq -c %s > %s || : > %s\n", shQuote(resourcesFilter), ResourcesFile, ResourcesFile)
	fmt.Fprintf(&b, "kubectl create secret generic %s --namespace %s --from-file=%s=%s --from-file=%s=%s --dry-run=client -o yaml | kubectl apply --namespace %s -f - || echo 'warning: failed to store outputs' >&2\n",
		shQuote(a.PlanArtifactSecret), shQuote(a.Namespace), OutputsKey, OutputsFile, ResourcesKey, ResourcesFile, shQuote(a.Namespace))
	return b.String()
}

// tfvarsFile renders the heredoc that writes Apply.TFVars into TFVarsFile.
// The JSON is compacted so the body is a single line (a value can't contain a
// raw newline, so it can never collide with the delimiter); JSON that fails
// to compact is written as-is — the workflow validation already rejected
// anything that is not an object.
func tfvarsFile(raw string) string {
	var buf bytes.Buffer
	body := raw
	if err := json.Compact(&buf, []byte(raw)); err == nil {
		body = buf.String()
	}
	return fmt.Sprintf("cat > %s <<'EOF'\n%s\nEOF\n", TFVarsFile, body)
}

// backendOverride renders the backend_override.tf the step writes before init.
// It emits the named backend (a.Backend, e.g. s3) with the BackendConfig
// key/values rendered as HCL strings, in sorted key order for a stable
// (testable) output — or, for managed state, the `http` backend pointed at
// StateAddress. The heredoc is single-quoted ('EOF') so nothing in the
// body is shell-expanded.
func backendOverride(a Apply) string {
	var body strings.Builder
	if a.Backend == BackendSpacefleet {
		// Managed state is OpenTofu's `http` backend. Only addresses and
		// methods are written: the credentials come from TF_HTTP_USERNAME /
		// TF_HTTP_PASSWORD in the environment.
		body.WriteString("terraform {\n  backend \"http\" {\n")
		fmt.Fprintf(&body, "    address        = %q\n", a.StateAddress)
		fmt.Fprintf(&body, "    lock_address   = %q\n", a.StateAddress+"/lock")
		fmt.Fprintf(&body, "    unlock_address = %q\n", a.StateAddress+"/lock")
		fmt.Fprintf(&body, "    lock_method    = %q\n", stateLockMethod)
		fmt.Fprintf(&body, "    unlock_method  = %q\n", stateUnlockMethod)
		body.WriteString("  }\n}\n")
	} else {
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
	}

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
