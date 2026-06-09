// Package applications holds the application use cases: registering an
// application in an organization and listing/fetching/updating/deleting them. An
// application is the owner of a deploy workflow (a DAG of Components, see
// lib/workflows); it holds only the workflow-owner fields — a name, an app-level
// default target cluster + namespace, and a runner cluster. The deploy steps and
// their chart config live on the components, and runs live on lib/workflows.
//
// Like every org-scoped resource, every query is scoped by organization id —
// that scoping, not the handler's membership check, is the security boundary.
package applications

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/application"
	"github.com/spacefleet/spacefleet/ent/applicationgroup"
	"github.com/spacefleet/spacefleet/ent/cluster"
)

// Service is a thin wrapper over the ent client.
type Service struct {
	ent *ent.Client
}

// NewService builds the application service over the ent client.
func NewService(entClient *ent.Client) *Service {
	return &Service{ent: entClient}
}

// ValidationError is a client-input error (bad/missing fields, an invalid
// cluster pairing) the handler maps to 400.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

// IsValidation reports whether err is a ValidationError.
func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

func validationErr(format string, args ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// CreateParams describes an application to register: the workflow-owner fields
// only. The deploy steps are added afterwards as components.
type CreateParams struct {
	Name            string
	TargetNamespace string
	TargetClusterID uuid.UUID
	RunnerClusterID uuid.UUID
	// GroupID optionally places the new application in a group (folder). Nil
	// creates it at the org root (ungrouped).
	GroupID *uuid.UUID
}

// UpdateParams describes a change to an application. A nil field is unchanged.
// The clusters are fixed at registration; name and target namespace can change.
type UpdateParams struct {
	Name            *string
	TargetNamespace *string
}

// ImportParams describes an application to adopt from a release already running
// on the target cluster (the import flow). Same shape as a create — the workflow
// itself is built afterwards from components.
type ImportParams = CreateParams

// List returns the organization's applications, oldest first.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]*ent.Application, error) {
	return s.ent.Application.Query().
		Where(application.OrganizationID(orgID)).
		Order(ent.Asc(application.FieldCreatedAt)).
		All(ctx)
}

// Get returns one application scoped to the organization, or ent's NotFoundError.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (*ent.Application, error) {
	return s.ent.Application.Query().
		Where(application.OrganizationID(orgID), application.ID(id)).
		Only(ctx)
}

// Create validates the cluster pairing, then registers the application. No
// workflow runs here — the deploy steps are added afterwards as components.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, p CreateParams) (*ent.Application, error) {
	if err := s.validate(ctx, orgID, p); err != nil {
		return nil, err
	}
	if err := s.ensureGroup(ctx, orgID, p.GroupID); err != nil {
		return nil, err
	}
	return s.newCreate(orgID, p).Save(ctx)
}

// Adopt registers an application from a release already running on the target
// cluster (the import flow). It validates the same cluster pairing as Create and
// creates the application in the imported state; the user then builds the
// deploy workflow from components.
func (s *Service) Adopt(ctx context.Context, orgID uuid.UUID, p ImportParams) (*ent.Application, error) {
	if err := s.validate(ctx, orgID, p); err != nil {
		return nil, err
	}
	if err := s.ensureGroup(ctx, orgID, p.GroupID); err != nil {
		return nil, err
	}
	return s.newCreate(orgID, p).
		SetImported(true).
		Save(ctx)
}

// newCreate builds the ent create shared by Create and Adopt from the validated
// params.
func (s *Service) newCreate(orgID uuid.UUID, p CreateParams) *ent.ApplicationCreate {
	c := s.ent.Application.Create().
		SetOrganizationID(orgID).
		SetName(p.Name).
		SetTargetNamespace(p.TargetNamespace).
		SetTargetClusterID(p.TargetClusterID).
		SetRunnerClusterID(p.RunnerClusterID)
	if p.GroupID != nil {
		c.SetGroupID(*p.GroupID)
	}
	return c
}

// Update changes mutable fields of an application scoped to the organization.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, p UpdateParams) (*ent.Application, error) {
	app, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	upd := app.Update()
	if p.Name != nil {
		upd.SetName(*p.Name)
	}
	if p.TargetNamespace != nil {
		if *p.TargetNamespace == "" {
			return nil, validationErr("target namespace cannot be empty")
		}
		upd.SetTargetNamespace(*p.TargetNamespace)
	}
	return upd.Save(ctx)
}

// Delete removes an application scoped to the organization.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	app, err := s.Get(ctx, orgID, id)
	if err != nil {
		return err
	}
	return s.ent.Application.DeleteOne(app).Exec(ctx)
}

// SetGroup moves an application into a group (groupID set) or to the org root
// (groupID nil), scoped to the organization. A non-nil group must belong to the
// same organization.
func (s *Service) SetGroup(ctx context.Context, orgID, id uuid.UUID, groupID *uuid.UUID) (*ent.Application, error) {
	app, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if err := s.ensureGroup(ctx, orgID, groupID); err != nil {
		return nil, err
	}
	upd := app.Update()
	if groupID != nil {
		upd.SetGroupID(*groupID)
	} else {
		upd.ClearGroupID()
	}
	return upd.Save(ctx)
}

// ensureGroup checks that a non-nil group exists in the organization (the
// tenancy boundary for the group reference). A nil group is the org root and
// always valid.
func (s *Service) ensureGroup(ctx context.Context, orgID uuid.UUID, groupID *uuid.UUID) error {
	if groupID == nil {
		return nil
	}
	exists, err := s.ent.ApplicationGroup.Query().
		Where(applicationgroup.OrganizationID(orgID), applicationgroup.ID(*groupID)).
		Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return validationErr("application group not found in this organization")
	}
	return nil
}

// validate checks the cluster pairing for a create.
func (s *Service) validate(ctx context.Context, orgID uuid.UUID, p CreateParams) error {
	if p.TargetNamespace == "" {
		return validationErr("target namespace is required")
	}
	target, err := s.ent.Cluster.Query().
		Where(cluster.OrganizationID(orgID), cluster.ID(p.TargetClusterID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return validationErr("target cluster not found in this organization")
		}
		return err
	}
	runner, err := s.ent.Cluster.Query().
		Where(cluster.OrganizationID(orgID), cluster.ID(p.RunnerClusterID)).
		WithTekton().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return validationErr("runner cluster not found in this organization")
		}
		return err
	}

	// In-cluster targets are only reachable from a pod in that same cluster, so
	// the runner must be that cluster. Use the token method for a remote runner.
	if target.ConnectionMethod == cluster.ConnectionMethodInCluster && runner.ID != target.ID {
		return validationErr("an in-cluster target requires the runner to be that same cluster; to use a different runner, register the target via the token method with an external endpoint")
	}
	// The runner must be designated to run jobs (Tekton enabled).
	if runner.Edges.Tekton == nil || !runner.Edges.Tekton.Enabled {
		return validationErr("runner cluster is not configured to run jobs (enable Tekton on it first)")
	}
	return nil
}
