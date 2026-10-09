package workflows

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// Sentinel errors the workflow validation returns, wrapped with detail, so a
// handler can map any of them to a 400. errors.Is against these classifies the
// failure.
var (
	// ErrDuplicateID is returned when two input stages or components share an id.
	ErrDuplicateID = errors.New("workflows: duplicate id")
	// ErrMissingID is returned when an input stage or component has the zero id.
	ErrMissingID = errors.New("workflows: id is required")
	// ErrInvalidStage is returned when a stage has no name or too long a one.
	ErrInvalidStage = errors.New("workflows: invalid stage")
	// ErrInvalidConfig is returned when a node's per-type config is missing a
	// required key or names an invalid value.
	ErrInvalidConfig = errors.New("workflows: invalid component config")
	// ErrInvalidTarget is returned when a node's target cluster is not in the
	// organization or violates the in-cluster/runner pairing rule.
	ErrInvalidTarget = errors.New("workflows: invalid component target")
	// ErrInvalidAction is returned by BeginRun for a run action that isn't one of
	// deploy / uninstall / preview. A handler maps it to 400.
	ErrInvalidAction = errors.New("workflows: invalid run action")
)

// Component types. helm runs a Helm release; manifest applies git-sourced
// Kubernetes manifests; terraform runs an OpenTofu plan/apply against a
// git-sourced root module. The set grows by adding a type here plus its config
// validation; persistence (a flat string config map) is unchanged.
const (
	TypeHelm      = "helm"
	TypeManifest  = "manifest"
	TypeTerraform = "terraform"
)

// validateConfig checks a node's per-type required config. helm requires a
// chart_source in {http_repo, oci, git} plus the keys that source needs (mirroring
// lib/applications' validateSourceConfig); manifest requires a repo_url + path.
// Adding a type is a new case here, not a migration.
func validateConfig(n ComponentInput) error {
	switch n.Type {
	case TypeHelm:
		return validateHelmConfig(n)
	case TypeManifest:
		return validateManifestConfig(n)
	case TypeTerraform:
		return validateTerraformConfig(n)
	default:
		return fmt.Errorf("%w: node %q has unknown type %q", ErrInvalidConfig, n.Name, n.Type)
	}
}

// helmConfigChartSource is the config key naming where a helm component's chart
// comes from, mirroring the old Application.chart_source column.
const helmConfigChartSource = "chart_source"

func validateHelmConfig(n ComponentInput) error {
	// A helm release deploys to a cluster + namespace, both required on the node.
	if !hasTargetCluster(n) {
		return fmt.Errorf("%w: node %q (helm) requires a target cluster", ErrInvalidConfig, n.Name)
	}
	if n.TargetNamespace == "" {
		return fmt.Errorf("%w: node %q (helm) requires a target namespace", ErrInvalidConfig, n.Name)
	}
	source := n.Config[helmConfigChartSource]
	switch source {
	case helm.SourceHTTPRepo:
		if err := requireConfig(n, helm.ConfigRepoURL, helm.ConfigChart); err != nil {
			return err
		}
	case helm.SourceOCI:
		if err := requireConfig(n, helm.ConfigRepoURL); err != nil {
			return err
		}
	case helm.SourceGit:
		if err := requireConfig(n, helm.ConfigRepoURL); err != nil {
			return err
		}
	case "":
		return fmt.Errorf("%w: node %q (helm) requires %q", ErrInvalidConfig, n.Name, helmConfigChartSource)
	default:
		return fmt.Errorf("%w: node %q (helm) has unknown %s %q", ErrInvalidConfig, n.Name, helmConfigChartSource, source)
	}
	if err := validateValuesSources(n); err != nil {
		return err
	}
	// ${{ }} interpolation references in values / release name / namespace are
	// rejected at save time when they could never render (malformed syntax,
	// misplaced run.* keys) — see lib/workflows/interpolation.go.
	return validateHelmInterpolation(n)
}

// validateValuesSources rejects a malformed values_sources at write time (F9) so a
// bad value can't slip through to the worker (where decodeValuesSources would only
// fail mid-run). It reuses the planner's decode and then checks each source carries
// the keys helm.Script requires (repo_url + path); git_ref is optional. An absent
// or empty value is fine (inline-only values). Failures wrap ErrInvalidConfig so a
// handler maps them to a 400.
func validateValuesSources(n ComponentInput) error {
	sources, err := decodeValuesSources(n.Config[helmConfigValuesSources])
	if err != nil {
		return fmt.Errorf("%w: node %q (helm): %v", ErrInvalidConfig, n.Name, err)
	}
	for i, src := range sources {
		for _, k := range []string{helm.ValuesSourceRepoURL, helm.ValuesSourcePath} {
			if src[k] == "" {
				return fmt.Errorf("%w: node %q (helm) %s[%d] requires %q", ErrInvalidConfig, n.Name, helmConfigValuesSources, i, k)
			}
		}
	}
	return nil
}

// manifestConfigPath is the config key for the path (file or directory) of
// manifests to apply within the cloned git repo.
const manifestConfigPath = "path"

func validateManifestConfig(n ComponentInput) error {
	// A manifest apply deploys to a cluster (its kubeconfig is what matters); the
	// namespace is informational (manifests carry their own), so it stays optional.
	if !hasTargetCluster(n) {
		return fmt.Errorf("%w: node %q (manifest) requires a target cluster", ErrInvalidConfig, n.Name)
	}
	return requireConfig(n, helm.ConfigRepoURL, manifestConfigPath)
}

// hasTargetCluster reports whether the node names a non-nil target cluster.
func hasTargetCluster(n ComponentInput) bool {
	return n.TargetClusterID != nil && *n.TargetClusterID != uuid.Nil
}

// Terraform component config keys. A terraform component clones a git repo at a
// ref, cds into a working path holding the root module (the repo root when
// empty), configures the state backend, and runs tofu plan or apply. The git
// source keys are shared with helm/manifest (helm.ConfigRepoURL /
// helm.ConfigGitRef) and the working-path key with manifest (manifestConfigPath).
const (
	// terraformConfigCommand selects the tofu verb an execution unit runs: "plan"
	// (produces the review material) or "apply" (mutates infrastructure). It is
	// NOT authored: an OpenTofu component is a single node that expandExecutionNodes
	// splits at run time into a plan unit and an apply unit (the apply gated by the
	// component's requires_approval) — this key is synthesized per unit there.
	terraformConfigCommand = "command"
	// terraformConfigBackend names the OpenTofu state backend Spacefleet
	// configures (it writes a backend_override.tf into the module, so state
	// lands where this config says regardless of any backend block in code).
	// Required; one of the tofu.Backend* values (see backendRequiredKeys).
	terraformConfigBackend = "backend"
	// terraformConfigBackendConfig is a JSON object of the backend's settings,
	// rendered into the generated backend_override.tf. For s3: bucket, key, and
	// region are required; dynamodb_table, encrypt, and kms_key_id are optional.
	// Secret values here get the same redaction treatment as helm inline values
	// (see lib/api/workflow.go secretConfigKeys).
	terraformConfigBackendConfig = "backend_config"
	// terraformConfigCloudCredentialID names an org-scoped cloud credential
	// (an aws credential id, a UUID) used to authenticate the run to the cloud —
	// the state backend and the module's AWS providers read it from the process
	// env. Optional (the runner may authenticate via an instance/IRSA role
	// instead). Not a secret — not redacted in lib/api/workflow.go.
	terraformConfigCloudCredentialID = "cloud_credential_id"
	// terraformConfigAuthClusterID optionally names a registered org cluster (a
	// UUID) whose authentication is made available to the run — "cluster
	// authentication" in the UI, for a module that drives Kubernetes-backed
	// providers (kubernetes/helm/kubectl). The resolver builds that cluster's
	// portable kubeconfig (minting any cloud token per attempt, exactly as a
	// helm target) and the script points KUBE_CONFIG_PATH at it. Internally it
	// rides the resolver's TargetClusterID path (see planTofu), but it is
	// deliberately NOT the node-level target_cluster_id — a terraform component
	// still has no deploy target; this only attaches auth. Not a secret — not
	// redacted in lib/api/workflow.go.
	terraformConfigAuthClusterID = "auth_cluster_id"
	// terraformConfigVersion selects the OpenTofu release line the component
	// runs (e.g. "1.12"); see lib/tofu's Version registry for the supported
	// list. Optional — empty runs the default line (tofu.DefaultVersion), so
	// components authored before this key existed keep their behavior. On
	// lines with native s3 locking (1.10+) the planner turns `use_lockfile`
	// on automatically.
	terraformConfigVersion = "tofu_version"
	// terraformConfigInitFlags / PlanFlags / ApplyFlags are optional JSON arrays
	// of extra CLI flag tokens appended to `tofu init` / `tofu plan` / `tofu apply`
	// respectively (after the flags Spacefleet sets itself). Each array element is
	// one whole argv token (e.g. "-var=env=prod", "-target=aws_instance.web"),
	// shell-quoted as a unit when rendered. Because an apply node applies the plan
	// node's SAVED planfile, plan-time flags (-var/-var-file/-target/-replace)
	// belong in plan_flags, not apply_flags; apply_flags is for flags valid against
	// a saved plan (e.g. -parallelism). plan_flags also apply to a preview plan.
	// Not secrets — not redacted in lib/api/workflow.go.
	terraformConfigInitFlags  = "init_flags"
	terraformConfigPlanFlags  = "plan_flags"
	terraformConfigApplyFlags = "apply_flags"
	// terraformConfigWorkspace optionally names the OpenTofu workspace every
	// unit of the component runs in (selected, or created on first use, right
	// after init). Empty = the default workspace. Lets one module back several
	// environments as separate components sharing a backend — the s3 backend
	// keys a workspace's state under env:/<workspace>/<key>. Validated as a
	// safe token (workspaceRe). Not a secret — not redacted.
	terraformConfigWorkspace = "workspace"
	// terraformConfigExposeTFVars ("true"/"false", default off) additionally
	// exports every variable the component resolves (group / app / component
	// levels merged) as TF_VAR_<name>, the environment form OpenTofu reads a
	// root-module input variable from — so the Variables feature doubles as
	// the module's inputs. See deploy.RunInputs.ExposeTFVars.
	terraformConfigExposeTFVars = "expose_tf_vars"
	// terraformConfigTFVars is an optional JSON object of typed root-module
	// input variables (name → any JSON value), written into the module as
	// spacefleet.auto.tfvars.json before every command — so a list, map, or
	// object input is authored as JSON rather than as HCL-in-a-string, and an
	// import resolves the same values a plan does. Validated as an object
	// whose keys are OpenTofu identifiers (tfVarNameRe). The file lands in
	// the rendered script, so this is for configuration, never secrets:
	// sensitive inputs go through Variables + expose_tf_vars (mounted from a
	// Secret). Not redacted.
	terraformConfigTFVars = "tfvars"
)

// tfVarNameRe is an OpenTofu identifier — what a root-module `variable`
// block can be named, and so what a tfvars key may be.
var tfVarNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// workspaceRe bounds a workspace name to what every backend accepts as a key
// segment: letters, digits, '-', '_' and '.', at most 90 characters (the
// name becomes part of the state object key).
var workspaceRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,90}$`)

// Terraform command values. state_op is the single unit of a guarded state
// operation run (see Service.BeginStateOp); the operation itself lives on the
// run row's args, not the node config.
const (
	terraformCommandPlan    = "plan"
	terraformCommandApply   = "apply"
	terraformCommandStateOp = "state_op"
)

// s3 backend_config keys the platform itself reads or writes — the rest of
// the object passes through to the rendered backend_override.tf verbatim.
const (
	// s3BackendKeyRegion is where the bucket (and any DynamoDB lock table)
	// lives; required, validated below.
	s3BackendKeyRegion = "region"
	// s3BackendKeyDynamoTable names the optional DynamoDB state-lock table.
	// When set with a cloud credential attached, the resolver ensures the
	// table exists before each run (first-party locking — see
	// cloudauth.EnsureLockTable).
	s3BackendKeyDynamoTable = "dynamodb_table"
	// s3BackendKeyLockfile is OpenTofu ≥1.10's native S3 locking switch. The
	// planner injects "true" automatically on lines that support it; an
	// explicit value here (API authors) is respected, and validation rejects
	// it on lines that would choke on the unknown backend argument.
	s3BackendKeyLockfile = "use_lockfile"
)

// validateTerraformConfig checks a terraform node's config: a git repo_url is
// required (the working path is optional — empty is the repo root), the state backend must be a supported type
// (spacefleet, s3, gcs, azurerm), and backend_config must be a JSON object
// carrying that backend's required settings (bucket/key/region for s3;
// nothing at all for spacefleet) — so a broken override is
// rejected at write time rather than failing mid-run in the worker. The
// plan/apply command is NOT authored: an OpenTofu component is one node,
// expanded into a plan unit + an apply unit at run time (see
// expandExecutionNodes), so command is synthesized, not validated here.
// Failures wrap ErrInvalidConfig so a handler maps them to a 400.
func validateTerraformConfig(n ComponentInput) error {
	// A terraform component manages cloud/infra, not a Kubernetes workload, so it
	// carries no cluster/namespace target — reject one if the builder sends it.
	if hasTargetCluster(n) {
		return fmt.Errorf("%w: node %q (terraform) must not set a target cluster", ErrInvalidConfig, n.Name)
	}
	if n.TargetNamespace != "" {
		return fmt.Errorf("%w: node %q (terraform) must not set a target namespace", ErrInvalidConfig, n.Name)
	}
	if err := requireConfig(n, helm.ConfigRepoURL); err != nil {
		return err
	}
	// The state backend is always managed by Spacefleet (the run writes a backend
	// override into the module), so the component must name a supported backend
	// type and supply its required settings — a broken or missing backend fails
	// here, at write time, not mid-run in the worker.
	backend := n.Config[terraformConfigBackend]
	required, ok := backendRequiredKeys[backend]
	if !ok {
		return fmt.Errorf("%w: node %q (terraform) %s must be one of %s", ErrInvalidConfig, n.Name, terraformConfigBackend, supportedBackends())
	}
	var backendCfg map[string]any
	if raw := n.Config[terraformConfigBackendConfig]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &backendCfg); err != nil {
			return fmt.Errorf("%w: node %q (terraform) %s must be a JSON object: %v", ErrInvalidConfig, n.Name, terraformConfigBackendConfig, err)
		}
	}
	// Managed state has no settings: Spacefleet owns the address, the lock,
	// and the credentials, so anything in backend_config would be ignored at
	// best — refuse it rather than let an author think it applies. Its
	// workspace becomes a path segment of the state address, so the two
	// names a path treats specially are refused too.
	if backend == tofu.BackendSpacefleet {
		if len(backendCfg) > 0 {
			return fmt.Errorf("%w: node %q (terraform) the %s state backend takes no %s; Spacefleet manages every setting", ErrInvalidConfig, n.Name, backend, terraformConfigBackendConfig)
		}
		if ws := n.Config[terraformConfigWorkspace]; ws == "." || ws == ".." {
			return fmt.Errorf("%w: node %q (terraform) %s %q is not allowed with the %s state backend", ErrInvalidConfig, n.Name, terraformConfigWorkspace, ws, backend)
		}
	}
	for _, key := range required {
		if s, _ := backendCfg[key].(string); s == "" {
			return fmt.Errorf("%w: node %q (terraform) the %s state backend requires %q in %s", ErrInvalidConfig, n.Name, backend, key, terraformConfigBackendConfig)
		}
	}
	// A secret never belongs in backend_config: the object is rendered into
	// the run's script (visible to editors and to anyone who can read
	// TaskRuns on the runner) and snapshotted onto every run. Each backend
	// reads the same setting from an environment variable, which a sensitive
	// Variable delivers from a mounted Secret — point there instead.
	for key, envName := range backendSecretKeys[backend] {
		if _, set := backendCfg[key]; set {
			return fmt.Errorf("%w: node %q (terraform) %s %q is a secret and cannot be stored in the backend settings; add a sensitive variable named %s to the component instead", ErrInvalidConfig, n.Name, terraformConfigBackendConfig, key, envName)
		}
	}
	// The OpenTofu line is optional (empty = the default line) but must be a
	// supported one, so an unknown value 400s at write time instead of failing
	// in the worker.
	version, ok := tofu.ResolveVersion(n.Config[terraformConfigVersion])
	if !ok {
		return fmt.Errorf("%w: node %q (terraform) %s %q is not supported (supported: %s)", ErrInvalidConfig, n.Name, terraformConfigVersion, n.Config[terraformConfigVersion], supportedTofuVersions())
	}
	// use_lockfile is meaningful only on lines with native s3 locking — older
	// lines fail `tofu init` on the unknown backend argument, so reject the
	// combination here with an actionable message instead.
	if _, set := backendCfg[s3BackendKeyLockfile]; set && backend == tofu.BackendS3 && !version.NativeS3Lock {
		return fmt.Errorf("%w: node %q (terraform) %s requires OpenTofu 1.10 or newer (%s is %q)", ErrInvalidConfig, n.Name, s3BackendKeyLockfile, terraformConfigVersion, version.Minor)
	}
	// A cloud credential is optional — the runner may authenticate via an
	// instance/IRSA role — but when present it must be a valid UUID.
	if id := n.Config[terraformConfigCloudCredentialID]; id != "" {
		if _, err := uuid.Parse(id); err != nil {
			return fmt.Errorf("%w: node %q (terraform) %s must be a UUID: %v", ErrInvalidConfig, n.Name, terraformConfigCloudCredentialID, err)
		}
	}
	// Cluster authentication is optional — when present it must be a valid UUID
	// (whether it names a real org cluster is resolved at run time, like the
	// cloud credential above).
	if id := n.Config[terraformConfigAuthClusterID]; id != "" {
		if _, err := uuid.Parse(id); err != nil {
			return fmt.Errorf("%w: node %q (terraform) %s must be a UUID: %v", ErrInvalidConfig, n.Name, terraformConfigAuthClusterID, err)
		}
	}
	// Optional per-command flag lists, each a JSON array of strings — reject a
	// malformed list at write time rather than failing mid-run in the worker.
	for _, key := range []string{terraformConfigInitFlags, terraformConfigPlanFlags, terraformConfigApplyFlags} {
		if raw := n.Config[key]; raw != "" {
			var flags []string
			if err := json.Unmarshal([]byte(raw), &flags); err != nil {
				return fmt.Errorf("%w: node %q (terraform) %s must be a JSON array of strings: %v", ErrInvalidConfig, n.Name, key, err)
			}
		}
	}
	// An optional workspace must be a safe token: it is shell-quoted into the
	// script and becomes part of the backend's state key.
	if ws := n.Config[terraformConfigWorkspace]; ws != "" && !workspaceRe.MatchString(ws) {
		return fmt.Errorf("%w: node %q (terraform) %s must be 1-90 letters, digits, '-', '_' or '.'", ErrInvalidConfig, n.Name, terraformConfigWorkspace)
	}
	switch n.Config[terraformConfigExposeTFVars] {
	case "", "true", "false":
	default:
		return fmt.Errorf("%w: node %q (terraform) %s must be \"true\" or \"false\"", ErrInvalidConfig, n.Name, terraformConfigExposeTFVars)
	}
	// Typed inputs: a JSON object keyed by variable name. Any JSON value is a
	// valid input (OpenTofu type-checks it against the declaration at plan
	// time); the key must be a name a variable block could carry.
	if raw := strings.TrimSpace(n.Config[terraformConfigTFVars]); raw != "" {
		var vars map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &vars); err != nil || vars == nil {
			return fmt.Errorf("%w: node %q (terraform) %s must be a JSON object of variable name to value", ErrInvalidConfig, n.Name, terraformConfigTFVars)
		}
		for name := range vars {
			if !tfVarNameRe.MatchString(name) {
				return fmt.Errorf("%w: node %q (terraform) %s key %q is not a valid variable name", ErrInvalidConfig, n.Name, terraformConfigTFVars, name)
			}
		}
	}
	return nil
}

// backendRequiredKeys lists, per supported state backend, the backend_config
// settings a component must supply; everything else in the object passes
// through to the rendered backend_override.tf verbatim (optional settings such
// as s3's dynamodb_table/encrypt/kms_key_id or azurerm's resource_group_name).
var backendRequiredKeys = map[string][]string{
	tofu.BackendS3:    {"bucket", "key", s3BackendKeyRegion},
	tofu.BackendGCS:   {"bucket", "prefix"},
	tofu.BackendAzure: {"storage_account_name", "container_name", "key"},
	// Managed state: no settings at all (validateTerraformConfig refuses any).
	tofu.BackendSpacefleet: {},
}

// backendSecretKeys lists, per backend, the backend_config settings that are
// secrets — refused at write time — each with the environment variable the
// backend reads the same value from, so the error can point the author at a
// sensitive Variable of that name (mounted from a Secret, never in the
// script). Non-secret key *names* (s3 kms_key_id, gcs kms_encryption_key)
// stay allowed.
var backendSecretKeys = map[string]map[string]string{
	tofu.BackendS3: {
		"access_key": "AWS_ACCESS_KEY_ID",
		"secret_key": "AWS_SECRET_ACCESS_KEY",
		"token":      "AWS_SESSION_TOKEN",
	},
	tofu.BackendGCS: {
		"encryption_key": "GOOGLE_ENCRYPTION_KEY",
		"credentials":    "GOOGLE_CREDENTIALS",
		"access_token":   "GOOGLE_OAUTH_ACCESS_TOKEN",
	},
	tofu.BackendAzure: {
		"access_key":                  "ARM_ACCESS_KEY",
		"sas_token":                   "ARM_SAS_TOKEN",
		"client_secret":               "ARM_CLIENT_SECRET",
		"client_certificate_password": "ARM_CLIENT_CERTIFICATE_PASSWORD",
		"oidc_token":                  "ARM_OIDC_TOKEN",
	},
}

// supportedBackends renders the supported backend names for the validation
// error, in a stable order.
func supportedBackends() string {
	return strings.Join([]string{tofu.BackendSpacefleet, tofu.BackendS3, tofu.BackendGCS, tofu.BackendAzure}, ", ")
}

// supportedTofuVersions renders the supported OpenTofu lines for the
// tofu_version validation error, newest first (e.g. "1.12, 1.11, 1.10, 1.9").
func supportedTofuVersions() string {
	minors := make([]string, 0, len(tofu.Versions))
	for _, v := range tofu.Versions {
		minors = append(minors, v.Minor)
	}
	return strings.Join(minors, ", ")
}

// requireConfig checks each named config key is present and non-empty on the node.
func requireConfig(n ComponentInput, keys ...string) error {
	for _, k := range keys {
		if n.Config[k] == "" {
			return fmt.Errorf("%w: node %q (%s) requires config %q", ErrInvalidConfig, n.Name, n.Type, k)
		}
	}
	return nil
}
