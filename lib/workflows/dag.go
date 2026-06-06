package workflows

import (
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
	// ErrInvalidAction is returned by BeginRun for a run action that isn't one of
	// deploy / uninstall / preview. A handler maps it to 400.
	ErrInvalidAction = errors.New("workflows: invalid run action")
)

// Component types. helm runs a Helm release; manifest applies git-sourced
// Kubernetes manifests. The set grows by adding a type here plus its config
// validation; persistence (a flat string config map) is unchanged.
const (
	TypeHelm     = "helm"
	TypeManifest = "manifest"
)

// validateDAG checks a proposed workflow is a well-formed DAG with valid per-type
// config: every node has a distinct non-zero id, every depends_on references an
// existing node, no node depends on itself, there are no cycles, and each node's
// config satisfies its type. It is a pure function (no ent, no I/O) so it is unit
// tested without a database. Errors wrap the sentinels above with the offending
// node for context.
func validateDAG(nodes []ComponentInput) error {
	ids := make(map[uuid.UUID]struct{}, len(nodes))
	for _, n := range nodes {
		if n.ID == uuid.Nil {
			return fmt.Errorf("%w: node %q", ErrMissingID, n.Name)
		}
		if _, dup := ids[n.ID]; dup {
			return fmt.Errorf("%w: %s", ErrDuplicateID, n.ID)
		}
		ids[n.ID] = struct{}{}
	}

	for _, n := range nodes {
		for _, dep := range n.DependsOn {
			if dep == n.ID {
				return fmt.Errorf("%w: %s (%q)", ErrSelfDependency, n.ID, n.Name)
			}
			if _, ok := ids[dep]; !ok {
				return fmt.Errorf("%w: node %q depends on %s", ErrUnknownDependency, n.Name, dep)
			}
		}
		if err := validateConfig(n); err != nil {
			return err
		}
	}

	if err := detectCycle(nodes); err != nil {
		return err
	}
	return nil
}

// detectCycle reports ErrCycle if the depends_on edges form a cycle, using Kahn's
// algorithm: repeatedly remove nodes with no remaining unmet dependency; if any
// node never becomes removable, the graph has a cycle. Assumes ids are unique and
// dependencies all resolve (validateDAG checks those first).
func detectCycle(nodes []ComponentInput) error {
	// indegree[id] = number of this node's dependencies still unsatisfied.
	indegree := make(map[uuid.UUID]int, len(nodes))
	// dependents[id] = nodes that depend on id (the reverse edges).
	dependents := make(map[uuid.UUID][]uuid.UUID, len(nodes))
	for _, n := range nodes {
		indegree[n.ID] = len(n.DependsOn)
		for _, dep := range n.DependsOn {
			dependents[dep] = append(dependents[dep], n.ID)
		}
	}

	queue := make([]uuid.UUID, 0, len(nodes))
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

	if resolved != len(nodes) {
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
		return requireConfig(n, helm.ConfigRepoURL, helm.ConfigChart)
	case helm.SourceOCI:
		return requireConfig(n, helm.ConfigRepoURL)
	case helm.SourceGit:
		return requireConfig(n, helm.ConfigRepoURL)
	case "":
		return fmt.Errorf("%w: node %q (helm) requires %q", ErrInvalidConfig, n.Name, helmConfigChartSource)
	default:
		return fmt.Errorf("%w: node %q (helm) has unknown %s %q", ErrInvalidConfig, n.Name, helmConfigChartSource, source)
	}
}

// manifestConfigPath is the config key for the path (file or directory) of
// manifests to apply within the cloned git repo.
const manifestConfigPath = "path"

func validateManifestConfig(n ComponentInput) error {
	return requireConfig(n, helm.ConfigRepoURL, manifestConfigPath)
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
