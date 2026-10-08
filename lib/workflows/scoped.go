package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/component"
	"github.com/spacefleet/spacefleet/ent/workflowstage"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// RunScope narrows a deploy or uninstall run to one OpenTofu component —
// a per-component destroy, or a deploy of just that module — optionally to
// a fixed list of resource addresses within it (`-target`). It is the
// per-run argument stored on the run row (args) of a component-scoped run;
// a run without one covers the whole workflow.
type RunScope struct {
	ComponentID uuid.UUID `json:"component_id"`
	// ComponentName is the authored name at the time the run began, kept so
	// the history reads without a lookup of a component that may since have
	// been renamed or deleted.
	ComponentName string `json:"component_name"`
	// Targets is the resource addresses the run is limited to, rendered as
	// -target flags on the plan; empty covers the whole component.
	Targets []string `json:"targets,omitempty"`
}

// ErrInvalidScopedAction is returned by BeginComponentRun for an action other
// than deploy or uninstall — the only two a component-scoped run performs.
// A handler maps it to 400.
var ErrInvalidScopedAction = errors.New("workflows: a component-scoped run is a deploy or an uninstall")

// BeginComponentRun opens a run limited to one OpenTofu component: a
// per-component **destroy** (action uninstall — `tofu plan -destroy` then
// the apply, gated) or a **targeted deploy** (action deploy, with the
// targets as `-target` flags on the plan; an empty list deploys the whole
// module). The component must belong to the org-scoped application (ent's
// NotFoundError otherwise) and be an OpenTofu component (ErrNotTofuComponent
// — a Helm or Manifest component has no state to target and may reference
// other components' outputs that only a whole-workflow run resolves). The
// targets are validated (tofu.ValidateTargets; failures wrap
// tofu.ErrInvalidTarget, a 400), and the application's in-flight gate
// applies exactly as for any run (ErrRunInFlight).
//
// The run is the component's usual plan → apply pair with no dependencies
// on anything else. A destroy is **always** gated (the apply parks for
// approval regardless of the component's own flag: the plan shows exactly
// what is about to be destroyed); a deploy keeps the component's own gate
// and approval policy. The scope is stored on the run row (args) as
// RunScope JSON for the history, and the targets are folded into the
// snapshot node's plan flags — the as-run config the planner already reads
// — so the worker needs nothing beyond the snapshot.
func (s *Service) BeginComponentRun(ctx context.Context, orgID, appID, componentID uuid.UUID, action string, targets []string) (*ent.WorkflowRun, error) {
	if action != ActionDeploy && action != ActionUninstall {
		return nil, ErrInvalidScopedAction
	}
	if err := tofu.ValidateTargets(targets); err != nil {
		return nil, err
	}
	if _, err := s.getApp(ctx, orgID, appID); err != nil {
		return nil, err
	}
	comp, err := s.ent.Component.Query().
		Where(component.OrganizationID(orgID), component.ApplicationID(appID), component.ID(componentID)).
		WithStage(func(q *ent.WorkflowStageQuery) { q.Where(workflowstage.OrganizationID(orgID)) }).
		Only(ctx)
	if err != nil {
		return nil, err
	}
	if string(comp.Type) != TypeTerraform {
		return nil, ErrNotTofuComponent
	}
	if err := s.assertNoRunInFlight(ctx, orgID, appID); err != nil {
		return nil, err
	}
	scope := RunScope{ComponentID: comp.ID, ComponentName: comp.Name, Targets: targets}
	args, err := json.Marshal(scope)
	if err != nil {
		return nil, err
	}
	return s.createRun(ctx, orgID, appID, action, string(args), scopedSnapshot(comp, action, targets))
}

// scopedSnapshot builds the snapshot of a component-scoped run from the live
// component: the authored node alone (its as-run config, credentials, and
// installation, with no dependencies — nothing else is in the run), with
// the targets appended to its plan flags and the gate forced on for a
// destroy, then expanded into its plan + apply units exactly as a
// whole-workflow run would expand it (the same unit ids, so the apply's
// recorded state lands under the component like any deploy). A pure
// function (unit-testable).
func scopedSnapshot(c *ent.Component, action string, targets []string) GraphSnapshot {
	n := GraphNode{
		ID:               c.ID,
		ComponentID:      c.ID,
		Name:             c.Name,
		Type:             string(c.Type),
		Config:           withTargets(nonNilStringMap(c.Config), targets),
		DependsOn:        []uuid.UUID{},
		RequiresApproval: c.RequiresApproval || action == ActionUninstall,
	}
	if !isZeroPolicy(c.ApprovalPolicy) {
		p := c.ApprovalPolicy
		n.ApprovalPolicy = &p
	}
	if c.GithubInstallationID != uuid.Nil {
		id := c.GithubInstallationID
		n.GitHubInstallationID = &id
	}
	stageID, stages := snapshotStageOf(c)
	n.StageID = stageID
	return GraphSnapshot{Stages: stages, Nodes: expandExecutionNodes([]GraphNode{n}, action)}
}

// withTargets returns a copy of a terraform config map whose plan_flags
// (a JSON string array, validated at write time) carry the targets as
// trailing `-target=<address>` flags. An empty target list returns the
// config unchanged (still a copy); unparseable existing flags are replaced
// rather than failing — the planner's own decode would have refused them.
// Never mutates the input map.
func withTargets(cfg map[string]string, targets []string) map[string]string {
	out := make(map[string]string, len(cfg)+1)
	for k, v := range cfg {
		out[k] = v
	}
	if len(targets) == 0 {
		return out
	}
	var flags []string
	if raw := cfg[terraformConfigPlanFlags]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &flags)
	}
	flags = append(flags, tofu.TargetFlags(targets)...)
	encoded, _ := json.Marshal(flags)
	out[terraformConfigPlanFlags] = string(encoded)
	return out
}

// ScopeOf decodes the scope stored on a component-scoped run's args. It
// returns ok=false for a run of any other action and for a whole-workflow
// deploy/uninstall (no args); a scoped run whose args do not decode is an
// error.
func ScopeOf(run *ent.WorkflowRun) (scope RunScope, ok bool, err error) {
	if a := string(run.Action); a != ActionDeploy && a != ActionUninstall {
		return RunScope{}, false, nil
	}
	if run.Args == "" {
		return RunScope{}, false, nil
	}
	if err := json.Unmarshal([]byte(run.Args), &scope); err != nil {
		return RunScope{}, false, fmt.Errorf("workflows: run %s: decode run scope: %w", run.ID, err)
	}
	if scope.ComponentID == uuid.Nil {
		return RunScope{}, false, fmt.Errorf("workflows: run %s: run scope names no component", run.ID)
	}
	return scope, true, nil
}
