// Package organizations holds the organization account-management use cases:
// creating an organization (which makes the creator its first admin), listing
// the organizations a user belongs to, managing the members of an organization,
// and admin-restricted mutations.
package organizations

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/membership"
)

// ErrForbidden is returned when a caller lacks the role required for an action
// (e.g. a non-admin trying to rename an organization). The API layer maps it to
// a 403.
var ErrForbidden = errors.New("organizations: forbidden")

// ErrLastAdmin is returned when removing or demoting a member would leave the
// organization with no admins. The API layer maps it to a 409. Every
// organization must keep at least one admin so it never becomes unmanageable.
var ErrLastAdmin = errors.New("organizations: organization must have at least one admin")

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

// Create makes a new organization and, in the same transaction, an admin
// membership for the creating user (the org's first admin).
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
		SetRole(membership.RoleAdmin).
		Save(ctx); err != nil {
		return nil, rollback(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return org, nil
}

// Rename changes an organization's name. Only an admin may do so: a non-member
// gets ent's NotFoundError, a non-admin member gets ErrForbidden.
func (s *Service) Rename(ctx context.Context, userID, orgID uuid.UUID, name string) (*ent.Organization, error) {
	m, err := s.Membership(ctx, userID, orgID)
	if err != nil {
		return nil, err
	}
	if m.Role != membership.RoleAdmin {
		return nil, ErrForbidden
	}
	return s.ent.Organization.UpdateOneID(orgID).SetName(name).Save(ctx)
}

// Get returns an organization by id. Callers that need authorization should
// resolve the caller's membership separately; this is a plain lookup (e.g. to
// read the org's name).
func (s *Service) Get(ctx context.Context, orgID uuid.UUID) (*ent.Organization, error) {
	return s.ent.Organization.Get(ctx, orgID)
}

// ListMembers returns the organization's members, oldest first, each with its
// user eager-loaded. Scoped by org id — the actual tenancy boundary.
func (s *Service) ListMembers(ctx context.Context, orgID uuid.UUID) ([]*ent.Membership, error) {
	return s.ent.Membership.Query().
		Where(membership.OrganizationID(orgID)).
		WithUser().
		Order(ent.Asc(membership.FieldCreatedAt)).
		All(ctx)
}

// SetMemberRole changes a member's role within an organization. Authorization
// (the caller being an admin) is the handler's job; this enforces the tenancy
// scope and the last-admin invariant. Demoting the only remaining admin returns
// ErrLastAdmin; a target who isn't a member returns ent's NotFoundError.
func (s *Service) SetMemberRole(ctx context.Context, orgID, targetUserID uuid.UUID, role membership.Role) (*ent.Membership, error) {
	m, err := s.Membership(ctx, targetUserID, orgID)
	if err != nil {
		return nil, err
	}
	if m.Role != role {
		if m.Role == membership.RoleAdmin && role != membership.RoleAdmin {
			n, err := s.countAdmins(ctx, orgID)
			if err != nil {
				return nil, err
			}
			if n <= 1 {
				return nil, ErrLastAdmin
			}
		}
		if err := s.ent.Membership.UpdateOneID(m.ID).SetRole(role).Exec(ctx); err != nil {
			return nil, err
		}
	}
	// Reload with the user edge so callers can render the member's email.
	return s.ent.Membership.Query().
		Where(membership.IDEQ(m.ID)).
		WithUser().
		Only(ctx)
}

// RemoveMember removes a member from an organization. Authorization is the
// handler's job; this enforces tenancy scope and the last-admin invariant.
// Removing the only remaining admin returns ErrLastAdmin; a non-member returns
// ent's NotFoundError.
func (s *Service) RemoveMember(ctx context.Context, orgID, targetUserID uuid.UUID) error {
	m, err := s.Membership(ctx, targetUserID, orgID)
	if err != nil {
		return err
	}
	if m.Role == membership.RoleAdmin {
		n, err := s.countAdmins(ctx, orgID)
		if err != nil {
			return err
		}
		if n <= 1 {
			return ErrLastAdmin
		}
	}
	return s.ent.Membership.DeleteOneID(m.ID).Exec(ctx)
}

// countAdmins counts the admins of an organization. Used to guard the
// last-admin invariant.
func (s *Service) countAdmins(ctx context.Context, orgID uuid.UUID) (int, error) {
	return s.ent.Membership.Query().
		Where(membership.OrganizationID(orgID), membership.RoleEQ(membership.RoleAdmin)).
		Count(ctx)
}

// rollback wraps tx.Rollback so a failed mutation surfaces both the original
// error and any rollback failure.
func rollback(tx *ent.Tx, err error) error {
	if rerr := tx.Rollback(); rerr != nil {
		return fmt.Errorf("%w: rollback: %v", err, rerr)
	}
	return err
}
