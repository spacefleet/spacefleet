// Package applicationgroups holds the application-group use cases: creating a
// top-level folder in an organization and listing/fetching/renaming/deleting
// them. A group is a purely organizational container for applications — an
// application points at a group via its group_id; nothing about deployment
// depends on it. Deleting a group ungroups its applications (ON DELETE SET NULL
// in the migration) rather than deleting them.
//
// Like every org-scoped resource, every query is scoped by organization id —
// that scoping, not the handler's membership check, is the security boundary.
package applicationgroups

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/applicationgroup"
)

// Service is a thin wrapper over the ent client.
type Service struct {
	ent *ent.Client
}

// NewService builds the application-group service over the ent client.
func NewService(entClient *ent.Client) *Service {
	return &Service{ent: entClient}
}

// ValidationError is a client-input error (bad/missing fields) the handler maps
// to 400.
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

// UpdateParams describes a change to a group. A nil field is unchanged; only the
// name is mutable.
type UpdateParams struct {
	Name *string
}

// List returns the organization's application groups, name ascending.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]*ent.ApplicationGroup, error) {
	return s.ent.ApplicationGroup.Query().
		Where(applicationgroup.OrganizationID(orgID)).
		Order(ent.Asc(applicationgroup.FieldName)).
		All(ctx)
}

// Get returns one group scoped to the organization, or ent's NotFoundError.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (*ent.ApplicationGroup, error) {
	return s.ent.ApplicationGroup.Query().
		Where(applicationgroup.OrganizationID(orgID), applicationgroup.ID(id)).
		Only(ctx)
}

// Create registers a group in the organization. Names are unique within the org
// (enforced by the DB; the handler maps the constraint violation to 409).
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, name string) (*ent.ApplicationGroup, error) {
	if name == "" {
		return nil, validationErr("name is required")
	}
	return s.ent.ApplicationGroup.Create().
		SetOrganizationID(orgID).
		SetName(name).
		Save(ctx)
}

// Update renames a group scoped to the organization.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, p UpdateParams) (*ent.ApplicationGroup, error) {
	g, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	upd := g.Update()
	if p.Name != nil {
		if *p.Name == "" {
			return nil, validationErr("name cannot be empty")
		}
		upd.SetName(*p.Name)
	}
	return upd.Save(ctx)
}

// Delete removes a group scoped to the organization. Its applications are not
// deleted — the group_id FK is ON DELETE SET NULL, so they fall back to the org
// root.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	g, err := s.Get(ctx, orgID, id)
	if err != nil {
		return err
	}
	return s.ent.ApplicationGroup.DeleteOne(g).Exec(ctx)
}
