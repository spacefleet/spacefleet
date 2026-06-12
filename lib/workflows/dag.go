package workflows

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// Sentinel errors the DAG validation returns, wrapped with detail, so a handler
// can map any of them to a 400. errors.Is against these classifies the failure.
var (
	// ErrDuplicateID is returned when two input nodes share the same id.
	ErrDuplicateID = errors.New("workflows: duplicate component id")
	// ErrMissingID is returned when an input node has the zero id.
	ErrMissingID = errors.New("workflows: component id is required")
	// ErrUnknownDependency is returned when a depends_on references an id that is
	// not among the input nodes.
	ErrUnknownDependency = errors.New("workflows: unknown dependency")
	// ErrSelfDependency is returned when a node depends on itself.
	ErrSelfDependency = errors.New("workflows: component depends on itself")
	// ErrCycle is returned when the depends_on edges form a cycle (not a DAG).
	ErrCycle = errors.New("workflows: workflow graph has a cycle")
	// ErrInvalidConfig is returned when a node's per-type config is missing a
	// required key or names an invalid value.
	ErrInvalidConfig = errors.New("workflows: invalid component config")
	// ErrInvalidTarget is returned when a node's target cluster is not in the
	// organization or violates the in-cluster/runner pairing rule.
	ErrInvalidTarget = errors.New("workflows: invalid component target")
	// ErrUnknownGroup is returned when a component's group_id names a group that
	// is not among the input groups.
	ErrUnknownGroup = errors.New("workflows: unknown group")
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

// validateDAG checks a proposed workflow (components only, no group containers)
// is a well-formed DAG with valid per-type config. It is the no-groups special
// case of validateWorkflow, kept as a thin alias so existing callers/tests read
// unchanged. It is a pure function (no ent, no I/O).
func validateDAG(nodes []ComponentInput) error {
	return validateWorkflow(nodes, nil)
}

// detectCycleAdj reports ErrCycle if the dependency edges in an already-expanded
// component-level adjacency map (node id → ids it depends on) form a cycle, using
// Kahn's algorithm: repeatedly remove nodes with no remaining unmet dependency;
// if any node never becomes removable, the graph has a cycle. Assumes the map's
// keys are the full node set and every dependency resolves to a key
// (validateWorkflow checks those first; expandDependencies yields only
// component ids, all of which are keys).
func detectCycleAdj(deps map[uuid.UUID][]uuid.UUID) error {
	// indegree[id] = number of this node's dependencies still unsatisfied.
	indegree := make(map[uuid.UUID]int, len(deps))
	// dependents[id] = nodes that depend on id (the reverse edges).
	dependents := make(map[uuid.UUID][]uuid.UUID, len(deps))
	for id, ds := range deps {
		indegree[id] = len(ds)
		for _, dep := range ds {
			dependents[dep] = append(dependents[dep], id)
		}
	}

	queue := make([]uuid.UUID, 0, len(deps))
	for id, deg := range indegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	resolved := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		resolved++
		for _, dependent := range dependents[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	if resolved != len(deps) {
		return ErrCycle
	}
	return nil
}

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
	return validateValuesSources(n)
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
// ref, cds into a working path holding the root module, configures the state
// backend, and runs tofu plan or apply. The git source keys are shared with
// helm/manifest (helm.ConfigRepoURL / helm.ConfigGitRef) and the working-path
// key with manifest (manifestConfigPath).
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
	// Required; tofu.BackendS3 is the only supported value today.
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
)

// Terraform command values.
const (
	terraformCommandPlan  = "plan"
	terraformCommandApply = "apply"
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

// validateTerraformConfig checks a terraform node's config: a git repo_url + a
// working path are required, the state backend must be a supported type (s3
// only today), and backend_config must be a JSON object carrying that backend's
// required settings (bucket/key/region for s3) — so a broken override is
// rejected at write time rather than failing mid-run in the worker. The
// plan/apply command is NOT authored: an OpenTofu component is one node,
// expanded into a plan unit + an apply unit at run time (see
// expandExecutionNodes), so command is synthesized, not validated here.
// Failures wrap ErrInvalidConfig so a handler maps them to a 400.
func validateTerraformConfig(n ComponentInput) error {
	// A terraform component manages cloud/infra, not a Kubernetes workload, so it
	// carries no cluster/namespace target — reject one if the canvas sends it.
	if hasTargetCluster(n) {
		return fmt.Errorf("%w: node %q (terraform) must not set a target cluster", ErrInvalidConfig, n.Name)
	}
	if n.TargetNamespace != "" {
		return fmt.Errorf("%w: node %q (terraform) must not set a target namespace", ErrInvalidConfig, n.Name)
	}
	if err := requireConfig(n, helm.ConfigRepoURL, manifestConfigPath); err != nil {
		return err
	}
	// The state backend is always managed by Spacefleet (the run writes a backend
	// override into the module), so the component must name a supported backend
	// type and supply its required settings — a broken or missing backend fails
	// here, at write time, not mid-run in the worker.
	if backend := n.Config[terraformConfigBackend]; backend != tofu.BackendS3 {
		return fmt.Errorf("%w: node %q (terraform) %s must be %q (the only supported state backend)", ErrInvalidConfig, n.Name, terraformConfigBackend, tofu.BackendS3)
	}
	var backendCfg map[string]any
	if raw := n.Config[terraformConfigBackendConfig]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &backendCfg); err != nil {
			return fmt.Errorf("%w: node %q (terraform) %s must be a JSON object: %v", ErrInvalidConfig, n.Name, terraformConfigBackendConfig, err)
		}
	}
	for _, key := range []string{"bucket", "key", s3BackendKeyRegion} {
		if s, _ := backendCfg[key].(string); s == "" {
			return fmt.Errorf("%w: node %q (terraform) the s3 state backend requires %q in %s", ErrInvalidConfig, n.Name, key, terraformConfigBackendConfig)
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
	if _, set := backendCfg[s3BackendKeyLockfile]; set && !version.NativeS3Lock {
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
	return nil
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
