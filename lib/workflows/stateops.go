package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/component"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// ErrNotTofuComponent is returned by BeginStateOp for a component that is
// not an OpenTofu component — there is no state to operate on. A handler
// maps it to 400.
var ErrNotTofuComponent = errors.New("workflows: state operations apply only to OpenTofu components")

// BeginStateOp opens a guarded state-operation run for one OpenTofu
// component: `tofu force-unlock`, `state rm`, `state mv`, or `import` — the
// things otherwise done from a laptop with production credentials — as a
// first-class, approval-gated, audited run. The operation is validated
// (tofu.StateOp.Validate; failures wrap tofu.ErrInvalidStateOp, a 400), the
// component must belong to the org-scoped application (ent's NotFoundError
// otherwise) and be an OpenTofu component (ErrNotTofuComponent), and the
// application's in-flight gate applies exactly as for any run
// (ErrRunInFlight): a state operation never runs beside a deploy.
//
// The run is one execution unit — the component with command=state_op —
// that is always gated (requires_approval regardless of the component's own
// flag: the gate is the whole point), so the run parks at awaiting_approval
// with the exact command on show before anything touches state. The
// operation's arguments are stored on the run row (args) as tofu.StateOp
// JSON — the durable source the worker plans from on the approval resume,
// not the job args. On success the unit hands the refreshed outputs and
// resource inventory back like a deploy apply, so the component's recorded
// state stays current.
func (s *Service) BeginStateOp(ctx context.Context, orgID, appID, componentID uuid.UUID, op tofu.StateOp) (*ent.WorkflowRun, error) {
	if err := op.Validate(); err != nil {
		return nil, err
	}
	if _, err := s.getApp(ctx, orgID, appID); err != nil {
		return nil, err
	}
	comp, err := s.ent.Component.Query().
		Where(component.OrganizationID(orgID), component.ApplicationID(appID), component.ID(componentID)).
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
	args, err := json.Marshal(op)
	if err != nil {
		return nil, err
	}
	return s.createRun(ctx, orgID, appID, ActionStateOp, string(args), stateOpSnapshot(comp, op))
}

// stateOpSnapshot builds the one-unit graph snapshot of a state-operation
// run from the live component: the authored node (its as-run config,
// credentials, and installation, so the planner resolves cloud auth and the
// backend exactly as a plan unit would) with command=state_op, no
// dependencies, no continue-on-failure, and the approval gate forced on. It
// keeps the authored id, so the step is found under the component like a
// plan or drift unit. A pure function (unit-testable).
func stateOpSnapshot(c *ent.Component, op tofu.StateOp) GraphSnapshot {
	n := GraphNode{
		ID:               c.ID,
		ComponentID:      c.ID,
		Name:             c.Name + " · " + op.Label(),
		Type:             string(c.Type),
		Config:           withCommand(nonNilStringMap(c.Config), terraformCommandStateOp),
		DependsOn:        []uuid.UUID{},
		RequiresApproval: true,
	}
	if !isZeroPolicy(c.ApprovalPolicy) {
		p := c.ApprovalPolicy
		n.ApprovalPolicy = &p
	}
	if c.GithubInstallationID != uuid.Nil {
		id := c.GithubInstallationID
		n.GitHubInstallationID = &id
	}
	if c.GroupID != uuid.Nil {
		id := c.GroupID
		n.GroupID = &id
	}
	return GraphSnapshot{Nodes: []GraphNode{n}}
}

// StateOpOf decodes the state operation stored on a state_op run's args. It
// returns ok=false for any other action (no args to read); a state_op run
// whose args do not decode is an error — the run cannot be planned.
func StateOpOf(run *ent.WorkflowRun) (op tofu.StateOp, ok bool, err error) {
	if string(run.Action) != ActionStateOp {
		return tofu.StateOp{}, false, nil
	}
	if run.Args == "" {
		return tofu.StateOp{}, false, fmt.Errorf("workflows: run %s has no state operation arguments", run.ID)
	}
	if err := json.Unmarshal([]byte(run.Args), &op); err != nil {
		return tofu.StateOp{}, false, fmt.Errorf("workflows: run %s: decode state operation: %w", run.ID, err)
	}
	return op, true, nil
}
