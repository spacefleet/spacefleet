// Package policies holds the policy use cases: an organization's Rego
// policies (see lib/policy for the engine) — listing, creating, updating,
// and deleting them, and resolving the enabled ones that apply to an
// application for the worker's plan gate.
//
// Like every org-scoped resource, every query is scoped by organization id
// — that scoping, not the handler's membership check, is the security
// boundary.
package policies

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/planpolicy"
	"github.com/spacefleet/spacefleet/lib/policy"
)

// Service is a thin wrapper over the ent client.
type Service struct {
	ent *ent.Client
}

func NewService(entClient *ent.Client) *Service {
	return &Service{ent: entClient}
}

// ValidationError is a client-input error the handler maps to 400.
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

// CreateParams describes a policy to create.
type CreateParams struct {
	Name          string
	Description   string
	Rego          string
	Enforcement   string
	Enabled       *bool
	ApplicationID uuid.UUID
}

// UpdateParams describes a change. A nil field is unchanged; ApplicationID
// set to uuid.Nil clears the application limit.
type UpdateParams struct {
	Name          *string
	Description   *string
	Rego          *string
	Enforcement   *string
	Enabled       *bool
	ApplicationID *uuid.UUID
}

// List returns the organization's policies, oldest first.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]*ent.PlanPolicy, error) {
	return s.ent.PlanPolicy.Query().
		Where(planpolicy.OrganizationID(orgID)).
		Order(ent.Asc(planpolicy.FieldCreatedAt)).
		All(ctx)
}

// Get returns one policy scoped to the organization, or ent's NotFoundError.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (*ent.PlanPolicy, error) {
	return s.ent.PlanPolicy.Query().
		Where(planpolicy.OrganizationID(orgID), planpolicy.ID(id)).
		Only(ctx)
}

// Create validates (the Rego must compile in package spacefleet) and stores
// a policy.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, p CreateParams) (*ent.PlanPolicy, error) {
	if strings.TrimSpace(p.Name) == "" {
		return nil, validationErr("name is required")
	}
	if p.Enforcement == "" {
		p.Enforcement = policy.EnforcementBlock
	}
	if !policy.ValidEnforcement(p.Enforcement) {
		return nil, validationErr("enforcement must be %s or %s", policy.EnforcementBlock, policy.EnforcementWarn)
	}
	if err := policy.Compile(p.Rego); err != nil {
		return nil, validationErr("%v", err)
	}
	create := s.ent.PlanPolicy.Create().
		SetOrganizationID(orgID).
		SetName(strings.TrimSpace(p.Name)).
		SetDescription(p.Description).
		SetRego(p.Rego).
		SetEnforcement(planpolicy.Enforcement(p.Enforcement))
	if p.Enabled != nil {
		create.SetEnabled(*p.Enabled)
	}
	if p.ApplicationID != uuid.Nil {
		if err := s.assertApp(ctx, orgID, p.ApplicationID); err != nil {
			return nil, err
		}
		create.SetApplicationID(p.ApplicationID)
	}
	return create.Save(ctx)
}

// Update changes mutable fields of a policy scoped to the organization.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, p UpdateParams) (*ent.PlanPolicy, error) {
	pol, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	upd := pol.Update()
	if p.Name != nil {
		if strings.TrimSpace(*p.Name) == "" {
			return nil, validationErr("name cannot be empty")
		}
		upd.SetName(strings.TrimSpace(*p.Name))
	}
	if p.Description != nil {
		upd.SetDescription(*p.Description)
	}
	if p.Rego != nil {
		if err := policy.Compile(*p.Rego); err != nil {
			return nil, validationErr("%v", err)
		}
		upd.SetRego(*p.Rego)
	}
	if p.Enforcement != nil {
		if !policy.ValidEnforcement(*p.Enforcement) {
			return nil, validationErr("enforcement must be %s or %s", policy.EnforcementBlock, policy.EnforcementWarn)
		}
		upd.SetEnforcement(planpolicy.Enforcement(*p.Enforcement))
	}
	if p.Enabled != nil {
		upd.SetEnabled(*p.Enabled)
	}
	if p.ApplicationID != nil {
		if *p.ApplicationID == uuid.Nil {
			upd.ClearApplicationID()
		} else {
			if err := s.assertApp(ctx, orgID, *p.ApplicationID); err != nil {
				return nil, err
			}
			upd.SetApplicationID(*p.ApplicationID)
		}
	}
	return upd.Save(ctx)
}

// Delete removes a policy scoped to the organization.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.ent.PlanPolicy.Delete().
		Where(planpolicy.OrganizationID(orgID), planpolicy.ID(id)).
		Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return &ent.NotFoundError{}
	}
	return nil
}

// assertApp checks an application belongs to the organization.
func (s *Service) assertApp(ctx context.Context, orgID, appID uuid.UUID) error {
	app, err := s.ent.Application.Get(ctx, appID)
	if err != nil && !ent.IsNotFound(err) {
		return err
	}
	if err != nil || app.OrganizationID != orgID {
		return validationErr("application not found")
	}
	return nil
}
