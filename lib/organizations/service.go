// Package organizations holds the organization account-management use cases:
// creating an organization (which makes the creator its owner), listing the
// organizations a user belongs to, and owner-restricted mutations.
package organizations

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/spacefleet/app/ent"
	"github.com/spacefleet/app/ent/membership"
)

// ErrForbidden is returned when a caller lacks the role required for an action
// (e.g. a non-owner trying to rename an organization). The API layer maps it to
// a 403.
var ErrForbidden = errors.New("organizations: forbidden")

// Service is a thin wrapper over the ent client.
type Service struct {
	ent *ent.Client
}

func NewService(entClient *ent.Client) *Service {
	return &Service{ent: entClient}
}

// ListForUser returns the user's memberships, oldest first, each with its
// organization eager-loaded. The role lives on the membership.
func (s *Service) ListForUser(ctx context.Context, userID uuid.UUID) ([]*ent.Membership, error) {
	return s.ent.Membership.Query().
		Where(membership.UserID(userID)).
		WithOrganization().
		Order(ent.Asc(membership.FieldCreatedAt)).
		All(ctx)
}

// Membership returns the caller's membership in an organization, or ent's
// NotFoundError if they don't belong to it. It's the authorization primitive
// for org-scoped resources: a handler resolves the requested org id and calls
// this to confirm access (and learn the caller's role) before proceeding.
func (s *Service) Membership(ctx context.Context, userID, orgID uuid.UUID) (*ent.Membership, error) {
	return s.ent.Membership.Query().
		Where(membership.UserID(userID), membership.OrganizationID(orgID)).
		Only(ctx)
}

// Create makes a new organization and, in the same transaction, an owner
// membership for the creating user.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, name string) (*ent.Organization, error) {
	tx, err := s.ent.Tx(ctx)
	if err != nil {
		return nil, err
	}
	org, err := tx.Organization.Create().SetName(name).Save(ctx)
	if err != nil {
		return nil, rollback(tx, err)
	}
	if _, err := tx.Membership.Create().
		SetOrganizationID(org.ID).
		SetUserID(userID).
		SetRole(membership.RoleOwner).
		Save(ctx); err != nil {
		return nil, rollback(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return org, nil
}

// Rename changes an organization's name. Only an owner may do so: a non-member
// gets ent's NotFoundError, a non-owner member gets ErrForbidden.
func (s *Service) Rename(ctx context.Context, userID, orgID uuid.UUID, name string) (*ent.Organization, error) {
	m, err := s.Membership(ctx, userID, orgID)
	if err != nil {
		return nil, err
	}
	if m.Role != membership.RoleOwner {
		return nil, ErrForbidden
	}
	return s.ent.Organization.UpdateOneID(orgID).SetName(name).Save(ctx)
}

// rollback wraps tx.Rollback so a failed mutation surfaces both the original
// error and any rollback failure.
func rollback(tx *ent.Tx, err error) error {
	if rerr := tx.Rollback(); rerr != nil {
		return fmt.Errorf("%w: rollback: %v", err, rerr)
	}
	return err
}
