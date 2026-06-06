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
	TargetClusterID      *uuid.UUID
	TargetNamespace      string
	ChartCredentialID    *uuid.UUID
	GitHubInstallationID *uuid.UUID
	Position             map[string]float64
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

// ReplaceWorkflow validates the proposed DAG and atomically replaces the
// application's components with it: in one transaction it deletes the app's
// existing components and recreates them from nodes, preserving each input's
// client-provided id (so depends_on edges and canvas identity are stable across an
// edit). A validation failure (see validateDAG) is returned before any write. The
// application must belong to the organization.
func (s *Service) ReplaceWorkflow(ctx context.Context, orgID, appID uuid.UUID, nodes []ComponentInput) ([]*ent.Component, error) {
	if _, err := s.getApp(ctx, orgID, appID); err != nil {
		return nil, err
	}
	if err := validateDAG(nodes); err != nil {
		return nil, err
	}

	tx, err := s.ent.Tx(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := tx.Component.Delete().
		Where(component.OrganizationID(orgID), component.ApplicationID(appID)).
		Exec(ctx); err != nil {
		return nil, rollback(tx, err)
	}

	for _, n := range nodes {
		if err := s.createComponent(ctx, tx, orgID, appID, n); err != nil {
			return nil, rollback(tx, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// Reload outside the transaction so callers see the persisted rows in order.
	return s.ent.Component.Query().
		Where(component.OrganizationID(orgID), component.ApplicationID(appID)).
		Order(ent.Asc(component.FieldCreatedAt)).
		All(ctx)
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
