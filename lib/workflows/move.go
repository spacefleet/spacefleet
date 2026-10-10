package workflows

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/component"
	tstate "github.com/spacefleet/spacefleet/ent/tofustate"
	"github.com/spacefleet/spacefleet/ent/variable"
	"github.com/spacefleet/spacefleet/ent/workflowstage"
	"github.com/spacefleet/spacefleet/lib/slug"
)

// ErrMoveTarget is returned by MoveComponent for a destination it can't use:
// an application not in the organization, a stage not in that application,
// or no stage named at all. A handler maps it to 400.
var ErrMoveTarget = errors.New("workflows: invalid move destination")

// ErrNameTaken is returned by MoveComponent when the destination application
// already has a component by the moved component's name. A handler maps it
// to 409.
var ErrNameTaken = errors.New("workflows: the application already has a component by that name")

// ErrComponentMoved is returned by a workflow save that carries a component
// which now lives in another application — a draft that was open while the
// component was moved. A handler maps it to 409.
var ErrComponentMoved = errors.New("workflows: component was moved to another application")

// ComponentMovedError names the moved component and where it went.
type ComponentMovedError struct {
	Component   string
	Application string
}

func (e *ComponentMovedError) Error() string {
	return fmt.Sprintf("%s was moved to %s — reload this workflow to see its current state", e.Component, e.Application)
}

func (e *ComponentMovedError) Unwrap() error { return ErrComponentMoved }

// MoveInput says where MoveComponent puts a component.
type MoveInput struct {
	// ApplicationID is the destination: another of the organization's
	// applications, or the component's own.
	ApplicationID uuid.UUID
	// StageID is a stage of the destination application, or nil with
	// NewStageName to add a stage at the end of its workflow.
	StageID      *uuid.UUID
	NewStageName string
	// Name renames the component on the way; empty keeps its name.
	Name string
}

// MoveComponent moves a component to the end of a stage of another of the
// organization's applications (or to another stage of its own), in one
// transaction. It keeps its id, so everything keyed by the id follows it:
// its component variables and its managed OpenTofu state are re-keyed to
// the destination, and its recorded state, drift checks, and locks are read
// by component id (see assertComponentInApp). Its settings travel
// unchanged. Its run history stays with the old application — those runs
// remain viewable there. A cloud backend's state is untouched: the config
// still points at the same bucket.
//
// Only structural validation applies — the name, and the target cluster
// against the destination's runner. References are not checked in either
// direction: a ${{ components.* }} reference that no longer resolves fails
// the next save of that workflow, or the run, and the user fixes it then.
//
// Refused while either application has a run in flight (ErrRunInFlight: a
// snapshot may still use the component, or its state), while the
// component's managed state is locked (StateLockedError), and when the
// destination already has a component by its name (ErrNameTaken).
func (s *Service) MoveComponent(ctx context.Context, orgID, appID, componentID uuid.UUID, in MoveInput) (*ent.Component, error) {
	comp, err := s.GetComponent(ctx, orgID, appID, componentID)
	if err != nil {
		return nil, err
	}
	target, err := s.getApp(ctx, orgID, in.ApplicationID)
	if ent.IsNotFound(err) {
		return nil, fmt.Errorf("%w: application not found", ErrMoveTarget)
	}
	if err != nil {
		return nil, err
	}
	name := comp.Name
	if n := strings.TrimSpace(in.Name); n != "" {
		name = n
	}
	if !slug.Valid(name) {
		return nil, fmt.Errorf("%w: component name %q %s", ErrInvalidConfig, name, slug.Rule)
	}
	newStage := strings.TrimSpace(in.NewStageName)
	if (in.StageID == nil) == (newStage == "") {
		return nil, fmt.Errorf("%w: name either a stage of the application or a new stage", ErrMoveTarget)
	}
	if utf8.RuneCountInString(newStage) > maxStageNameLen {
		return nil, fmt.Errorf("%w: stage name %q is longer than %d characters", ErrInvalidStage, newStage, maxStageNameLen)
	}
	if in.StageID != nil {
		ok, err := s.ent.WorkflowStage.Query().
			Where(
				workflowstage.OrganizationID(orgID),
				workflowstage.ApplicationID(target.ID),
				workflowstage.ID(*in.StageID),
			).
			Exist(ctx)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: that stage is not in %s's workflow", ErrMoveTarget, target.Name)
		}
		if target.ID == appID && *in.StageID == comp.StageID && name == comp.Name {
			return comp, nil // already there
		}
	}
	taken, err := s.ent.Component.Query().
		Where(
			component.OrganizationID(orgID),
			component.ApplicationID(target.ID),
			component.Name(name),
			component.IDNEQ(comp.ID),
		).
		Exist(ctx)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, fmt.Errorf("%w: %s already has a component named %s", ErrNameTaken, target.Name, name)
	}
	if err := s.validateComponentTargets(ctx, orgID, target, []ComponentInput{movedInput(comp, name)}); err != nil {
		return nil, err
	}
	if err := s.assertNoRunInFlight(ctx, orgID, appID); err != nil {
		return nil, err
	}
	if target.ID != appID {
		if err := s.assertNoRunInFlight(ctx, orgID, target.ID); err != nil {
			return nil, fmt.Errorf("%w (in %s)", err, target.Name)
		}
	}
	locked, err := s.ent.TofuState.Query().
		Where(
			tstate.OrganizationID(orgID),
			tstate.ApplicationID(appID),
			tstate.ComponentID(comp.ID),
			tstate.LockIDNotNil(),
		).
		Exist(ctx)
	if err != nil {
		return nil, err
	}
	if locked {
		return nil, &StateLockedError{Component: comp.Name}
	}

	tx, err := s.ent.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var stageID uuid.UUID
	if in.StageID != nil {
		stageID = *in.StageID
	} else {
		count, err := tx.WorkflowStage.Query().
			Where(workflowstage.OrganizationID(orgID), workflowstage.ApplicationID(target.ID)).
			Count(ctx)
		if err != nil {
			return nil, rollback(tx, err)
		}
		st, err := tx.WorkflowStage.Create().
			SetOrganizationID(orgID).
			SetApplicationID(target.ID).
			SetName(newStage).
			SetOrdinal(count).
			Save(ctx)
		if err != nil {
			return nil, rollback(tx, err)
		}
		stageID = st.ID
	}
	ordinal := comp.Ordinal
	if stageID != comp.StageID {
		// The end of the destination stage.
		if ordinal, err = tx.Component.Query().
			Where(component.OrganizationID(orgID), component.StageID(stageID)).
			Count(ctx); err != nil {
			return nil, rollback(tx, err)
		}
	}
	if err := tx.Component.UpdateOneID(comp.ID).
		Where(component.OrganizationID(orgID)).
		SetApplicationID(target.ID).
		SetStageID(stageID).
		SetOrdinal(ordinal).
		SetName(name).
		Exec(ctx); err != nil {
		return nil, rollback(tx, err)
	}
	if target.ID != appID {
		if _, err := tx.Variable.Update().
			Where(
				variable.OrganizationID(orgID),
				variable.ApplicationID(appID),
				variable.ComponentID(comp.ID),
			).
			SetApplicationID(target.ID).
			Save(ctx); err != nil {
			return nil, rollback(tx, err)
		}
		if _, err := tx.TofuState.Update().
			Where(
				tstate.OrganizationID(orgID),
				tstate.ApplicationID(appID),
				tstate.ComponentID(comp.ID),
				tstate.LockIDIsNil(),
			).
			SetApplicationID(target.ID).
			Save(ctx); err != nil {
			return nil, rollback(tx, err)
		}
		// A lock taken since the check above would have kept its row behind.
		left, err := tx.TofuState.Query().
			Where(tstate.OrganizationID(orgID), tstate.ApplicationID(appID), tstate.ComponentID(comp.ID)).
			Exist(ctx)
		if err != nil {
			return nil, rollback(tx, err)
		}
		if left {
			return nil, rollback(tx, &StateLockedError{Component: comp.Name})
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetComponent(ctx, orgID, target.ID, comp.ID)
}

// movedInput is the part of a stored component validateComponentTargets
// reads, under its new name.
func movedInput(c *ent.Component, name string) ComponentInput {
	in := ComponentInput{ID: c.ID, Name: name, Type: string(c.Type), Config: c.Config}
	if c.TargetClusterID != uuid.Nil {
		id := c.TargetClusterID
		in.TargetClusterID = &id
	}
	return in
}

// checkMovedComponents refuses a workflow save that carries a component now
// living in another of the organization's applications — the save would
// otherwise try to recreate it under this one. That happens when a builder
// was open while the component was moved away.
func (s *Service) checkMovedComponents(ctx context.Context, orgID, appID uuid.UUID, nodes []ComponentInput) error {
	if len(nodes) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	moved, err := s.ent.Component.Query().
		Where(
			component.OrganizationID(orgID),
			component.IDIn(ids...),
			component.ApplicationIDNEQ(appID),
		).
		WithApplication().
		First(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	where := "another application"
	if moved.Edges.Application != nil {
		where = moved.Edges.Application.Name
	}
	return &ComponentMovedError{Component: moved.Name, Application: where}
}
