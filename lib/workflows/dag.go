package workflows

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/lib/helm"
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
	return requireConfig(n, helm.ConfigRepoURL, manifestConfigPath)
}

// Terraform component config keys. A terraform component clones a git repo at a
// ref, cds into a working path holding the root module, configures the state
// backend, and runs tofu plan or apply. The git source keys are shared with
// helm/manifest (helm.ConfigRepoURL / helm.ConfigGitRef) and the working-path
// key with manifest (manifestConfigPath).
const (
	// terraformConfigCommand selects the tofu verb the node runs: "plan"
	// (produces the review material) or "apply" (mutates infrastructure). A
	// terraform deployment is modelled as two nodes — a plan node and an apply
	// node that depends on it and is gated by requires_approval.
	terraformConfigCommand = "command"
	// terraformConfigBackend names the OpenTofu state backend. Defaults to
	// "kubernetes" (state stored as Secrets in the runner cluster) when empty.
	terraformConfigBackend = "backend"
	// terraformConfigBackendConfig is an optional JSON object of backend
	// settings (e.g. S3 bucket/region, pg conn_str). Rendered into the generated
	// backend_override.tf. Secret values here get the same redaction treatment as
	// helm inline values (see lib/api/workflow.go secretConfigKeys).
	terraformConfigBackendConfig = "backend_config"
)

// Terraform command values.
const (
	terraformCommandPlan  = "plan"
	terraformCommandApply = "apply"
)

// validateTerraformConfig checks a terraform node's config: a git repo_url + a
// working path are required; command must be plan or apply; and backend_config,
// when present, must parse as a JSON object (so a malformed override is rejected
// at write time rather than failing mid-run in the worker). backend is optional
// (defaults to kubernetes). Failures wrap ErrInvalidConfig so a handler maps
// them to a 400.
func validateTerraformConfig(n ComponentInput) error {
	if err := requireConfig(n, helm.ConfigRepoURL, manifestConfigPath); err != nil {
		return err
	}
	switch n.Config[terraformConfigCommand] {
	case terraformCommandPlan, terraformCommandApply:
	case "":
		return fmt.Errorf("%w: node %q (terraform) requires %q (one of %q, %q)", ErrInvalidConfig, n.Name, terraformConfigCommand, terraformCommandPlan, terraformCommandApply)
	default:
		return fmt.Errorf("%w: node %q (terraform) has unknown %s %q", ErrInvalidConfig, n.Name, terraformConfigCommand, n.Config[terraformConfigCommand])
	}
	if raw := n.Config[terraformConfigBackendConfig]; raw != "" {
		var obj map[string]any
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			return fmt.Errorf("%w: node %q (terraform) %s must be a JSON object: %v", ErrInvalidConfig, n.Name, terraformConfigBackendConfig, err)
		}
	}
	return nil
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
