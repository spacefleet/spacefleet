// Package workflows holds the application deploy-workflow use cases: an
// application owns a DAG of typed Components (helm release, manifest apply, …),
// and a run of that workflow is a WorkflowRun with one ComponentRun per node. This
// package owns the component CRUD, the write-time DAG validation, and (in later
// phases) the run snapshot + status marking the worker drives through.
//
// It is a thin wrapper over the ent client. Like every org-scoped resource, every
// query is scoped by organization id — that scoping, not a handler's membership
// check, is the security boundary. A component carries no secrets of its own; the
// credentials it references (chart credential, GitHub installation) and the target
// cluster are validated and resolved elsewhere, the same way lib/applications does.
package workflows

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/application"
	"github.com/spacefleet/spacefleet/ent/component"
	"github.com/spacefleet/spacefleet/ent/componentgroup"
	"github.com/spacefleet/spacefleet/ent/variable"
)

// Service is a thin wrapper over the ent client.
type Service struct {
	ent *ent.Client
}

// NewService builds the workflow service over the ent client.
func NewService(entClient *ent.Client) *Service {
	return &Service{ent: entClient}
}

// ComponentInput is one node of a proposed workflow, as supplied by the canvas.
// ID is client-provided (the canvas assigns a stable uuid per node) so depends_on
// can reference sibling nodes across an edit and so identity survives a replace.
// Config is the type-specific, non-secret param map, validated per type.
type ComponentInput struct {
	ID                   uuid.UUID
	Name                 string
	Type                 string
	Config               map[string]string
	DependsOn            []uuid.UUID
	ContinueOnFailure    bool
	RequiresApproval     bool
	TargetClusterID      *uuid.UUID
	TargetNamespace      string
	ChartCredentialID    *uuid.UUID
	GitHubInstallationID *uuid.UUID
	Position             map[string]float64
	// GroupID is the explicit group container this component is a member of, or
	// nil when ungrouped. Members of a group run in parallel and a node depending
	// on the group waits for all of them.
	GroupID *uuid.UUID
}

// ListComponents returns an application's workflow nodes, scoped to the
// organization. It first confirms the application belongs to the org (so a caller
// can't read another org's components by id); a missing application surfaces as
// ent's NotFoundError.
func (s *Service) ListComponents(ctx context.Context, orgID, appID uuid.UUID) ([]*ent.Component, error) {
	if _, err := s.getApp(ctx, orgID, appID); err != nil {
		return nil, err
	}
	return s.ent.Component.Query().
		Where(component.OrganizationID(orgID), component.ApplicationID(appID)).
		Order(ent.Asc(component.FieldCreatedAt)).
		All(ctx)
}

// ReplaceWorkflow validates the proposed authored graph (components + explicit
// group containers) and atomically replaces the application's workflow with it:
// in one transaction it deletes the app's existing groups and components and
// recreates them, preserving each input's client-provided id (so depends_on
// edges, group_id refs, and canvas identity are stable across an edit). Groups
// are deleted/recreated before components because components carry a group_id FK
// to component_groups. A validation failure (see validateWorkflow) is returned
// before any write. The application must belong to the organization.
//
// Returns the persisted components and groups (created-order), reloaded outside
// the transaction.
func (s *Service) ReplaceWorkflow(ctx context.Context, orgID, appID uuid.UUID, nodes []ComponentInput, groups []GroupInput) ([]*ent.Component, []*ent.ComponentGroup, error) {
	if _, err := s.getApp(ctx, orgID, appID); err != nil {
		return nil, nil, err
	}
	if err := validateWorkflow(nodes, groups); err != nil {
		return nil, nil, err
	}

	tx, err := s.ent.Tx(ctx)
	if err != nil {
		return nil, nil, err
	}

	// Delete components first (they FK group_id → component_groups), then groups.
	if _, err := tx.Component.Delete().
		Where(component.OrganizationID(orgID), component.ApplicationID(appID)).
		Exec(ctx); err != nil {
		return nil, nil, rollback(tx, err)
	}
	if _, err := tx.ComponentGroup.Delete().
		Where(componentgroup.OrganizationID(orgID), componentgroup.ApplicationID(appID)).
		Exec(ctx); err != nil {
		return nil, nil, rollback(tx, err)
	}

	// Recreate groups before components, since a component's group_id references a
	// group row.
	for _, g := range groups {
		if err := s.createGroup(ctx, tx, orgID, appID, g); err != nil {
			return nil, nil, rollback(tx, err)
		}
	}
	for _, n := range nodes {
		if err := s.createComponent(ctx, tx, orgID, appID, n); err != nil {
			return nil, nil, rollback(tx, err)
		}
	}

	// Reconcile component-scoped variables: drop those whose component is no
	// longer in the workflow. Component variables aren't FK'd to components (the
	// components are deleted + recreated above with stable ids), so a variable
	// whose component_id is absent from the new node set belongs to a removed
	// component and must be cleaned up. Components that persist keep their id, so
	// their variables survive untouched.
	del := tx.Variable.Delete().Where(
		variable.OrganizationID(orgID),
		variable.ApplicationID(appID),
		variable.ComponentIDNotNil(),
	)
	if len(nodes) > 0 {
		keep := make([]uuid.UUID, len(nodes))
		for i, n := range nodes {
			keep[i] = n.ID
		}
		del = del.Where(variable.ComponentIDNotIn(keep...))
	}
	if _, err := del.Exec(ctx); err != nil {
		return nil, nil, rollback(tx, err)
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	// Reload outside the transaction so callers see the persisted rows in order.
	comps, err := s.ent.Component.Query().
		Where(component.OrganizationID(orgID), component.ApplicationID(appID)).
		Order(ent.Asc(component.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}
	grps, err := s.listGroups(ctx, orgID, appID)
	if err != nil {
		return nil, nil, err
	}
	return comps, grps, nil
}

// ListGroups returns an application's group containers, scoped to the
// organization. It first confirms the application belongs to the org; a missing
// application surfaces as ent's NotFoundError.
func (s *Service) ListGroups(ctx context.Context, orgID, appID uuid.UUID) ([]*ent.ComponentGroup, error) {
	if _, err := s.getApp(ctx, orgID, appID); err != nil {
		return nil, err
	}
	return s.listGroups(ctx, orgID, appID)
}

// listGroups is the unguarded org-scoped query, shared by ListGroups (which adds
// the application-membership check) and ReplaceWorkflow (which already confirmed
// the app).
func (s *Service) listGroups(ctx context.Context, orgID, appID uuid.UUID) ([]*ent.ComponentGroup, error) {
	return s.ent.ComponentGroup.Query().
		Where(componentgroup.OrganizationID(orgID), componentgroup.ApplicationID(appID)).
		Order(ent.Asc(componentgroup.FieldCreatedAt)).
		All(ctx)
}

// createGroup persists one validated group container within the replace
// transaction, keeping its client-provided id.
func (s *Service) createGroup(ctx context.Context, tx *ent.Tx, orgID, appID uuid.UUID, g GroupInput) error {
	return tx.ComponentGroup.Create().
		SetID(g.ID).
		SetOrganizationID(orgID).
		SetApplicationID(appID).
		SetName(g.Name).
		SetDependsOn(nonNilIDs(g.DependsOn)).
		SetPosition(nonNilFloatMap(g.Position)).
		SetSize(nonNilFloatMap(g.Size)).
		Exec(ctx)
}

// createComponent persists one validated node within the replace transaction,
// keeping its client-provided id. Optional FK fields (target cluster, credential,
// installation) are set only when present and non-zero, mirroring lib/applications.
func (s *Service) createComponent(ctx context.Context, tx *ent.Tx, orgID, appID uuid.UUID, n ComponentInput) error {
	create := tx.Component.Create().
		SetID(n.ID).
		SetOrganizationID(orgID).
		SetApplicationID(appID).
		SetName(n.Name).
		SetType(component.Type(n.Type)).
		SetConfig(nonNilStringMap(n.Config)).
		SetDependsOn(nonNilIDs(n.DependsOn)).
		SetContinueOnFailure(n.ContinueOnFailure).
		SetRequiresApproval(n.RequiresApproval).
		SetTargetNamespace(n.TargetNamespace).
		SetPosition(nonNilFloatMap(n.Position))
	if n.TargetClusterID != nil && *n.TargetClusterID != uuid.Nil {
		create.SetTargetClusterID(*n.TargetClusterID)
	}
	if n.ChartCredentialID != nil && *n.ChartCredentialID != uuid.Nil {
		create.SetChartCredentialID(*n.ChartCredentialID)
	}
	if n.GitHubInstallationID != nil && *n.GitHubInstallationID != uuid.Nil {
		create.SetGithubInstallationID(*n.GitHubInstallationID)
	}
	if n.GroupID != nil && *n.GroupID != uuid.Nil {
		create.SetGroupID(*n.GroupID)
	}
	return create.Exec(ctx)
}

// getApp confirms the application exists in the organization, returning ent's
// NotFoundError otherwise. The scoped query is the tenancy boundary for every
// workflow operation on the app.
func (s *Service) getApp(ctx context.Context, orgID, appID uuid.UUID) (*ent.Application, error) {
	return s.ent.Application.Query().
		Where(application.OrganizationID(orgID), application.ID(appID)).
		Only(ctx)
}

// nonNilStringMap guards against a nil map reaching the JSON column.
func nonNilStringMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// nonNilFloatMap guards against a nil map reaching the JSON column.
func nonNilFloatMap(m map[string]float64) map[string]float64 {
	if m == nil {
		return map[string]float64{}
	}
	return m
}

// nonNilIDs guards against a nil slice reaching the JSON column.
func nonNilIDs(s []uuid.UUID) []uuid.UUID {
	if s == nil {
		return []uuid.UUID{}
	}
	return s
}

// rollback wraps tx.Rollback so a failed mutation surfaces both the original
// error and any rollback failure.
func rollback(tx *ent.Tx, err error) error {
	if rerr := tx.Rollback(); rerr != nil {
		return fmt.Errorf("%w: rollback: %v", err, rerr)
	}
	return err
}
