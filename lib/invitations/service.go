// Package invitations holds the organization-invitation use cases: creating an
// invitation (with a bearer token embedded in the invite link), listing and
// revoking pending invitations, and accepting one to join an organization.
//
// The token is the sole authority on accept: whoever holds a valid, unexpired
// link and is signed in joins with the invitation's role. The recorded email is
// for display and email delivery only — it is not checked against the accepting
// user. Invitations expire 30 days after creation and are marked
// accepted/revoked rather than deleted so admins can see the history.
package invitations

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/invitation"
	"github.com/spacefleet/spacefleet/ent/membership"
)

// TTL is how long an invitation stays valid after creation.
const TTL = 30 * 24 * time.Hour

// ErrNotPending is returned when accepting an invitation that has already been
// accepted or revoked. The API layer maps it to 410 Gone.
var ErrNotPending = errors.New("invitations: invitation is no longer pending")

// ErrExpired is returned when accepting an invitation past its expiry. The API
// layer maps it to 410 Gone.
var ErrExpired = errors.New("invitations: invitation has expired")

// Service is a thin wrapper over the ent client.
type Service struct {
	ent *ent.Client
}

func NewService(entClient *ent.Client) *Service {
	return &Service{ent: entClient}
}

// Create issues a new pending invitation for email to join orgID with role.
// Any existing pending invitation for the same address in the same org is
// revoked first, so an admin re-inviting someone always produces exactly one
// live link. invitedBy records who issued it (for audit); pass uuid.Nil to omit.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, email string, role invitation.Role, invitedBy uuid.UUID) (*ent.Invitation, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, errors.New("invitations: email is required")
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}

	tx, err := s.ent.Tx(ctx)
	if err != nil {
		return nil, err
	}
	// Supersede any live invitation for this address so only one link works.
	if _, err := tx.Invitation.Update().
		Where(
			invitation.OrganizationID(orgID),
			invitation.EmailEQ(email),
			invitation.StatusEQ(invitation.StatusPending),
		).
		SetStatus(invitation.StatusRevoked).
		Save(ctx); err != nil {
		return nil, rollback(tx, err)
	}

	create := tx.Invitation.Create().
		SetOrganizationID(orgID).
		SetEmail(email).
		SetRole(role).
		SetToken(token).
		SetExpiresAt(time.Now().Add(TTL))
	if invitedBy != uuid.Nil {
		create = create.SetInvitedBy(invitedBy)
	}
	inv, err := create.Save(ctx)
	if err != nil {
		return nil, rollback(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return inv, nil
}

// ListPending returns an organization's pending invitations, newest first.
// Scoped by org id — the actual tenancy boundary.
func (s *Service) ListPending(ctx context.Context, orgID uuid.UUID) ([]*ent.Invitation, error) {
	return s.ent.Invitation.Query().
		Where(
			invitation.OrganizationID(orgID),
			invitation.StatusEQ(invitation.StatusPending),
		).
		Order(ent.Desc(invitation.FieldCreatedAt)).
		All(ctx)
}

// Revoke marks a pending invitation revoked, invalidating its link. Scoped by
// org id; a missing or already-resolved invitation returns ent's NotFoundError.
func (s *Service) Revoke(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.ent.Invitation.Update().
		Where(
			invitation.IDEQ(id),
			invitation.OrganizationID(orgID),
			invitation.StatusEQ(invitation.StatusPending),
		).
		SetStatus(invitation.StatusRevoked).
		Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return &ent.NotFoundError{}
	}
	return nil
}

// Lookup returns the invitation for a token with its organization eager-loaded.
// Not org-scoped — the token is the lookup key for the accept flow.
func (s *Service) Lookup(ctx context.Context, token string) (*ent.Invitation, error) {
	return s.ent.Invitation.Query().
		Where(invitation.TokenEQ(token)).
		WithOrganization().
		Only(ctx)
}

// Accept joins userID to the invitation's organization with the invitation's
// role and marks it accepted, all in one transaction. It is idempotent for a
// user who is already a member (their existing role is left unchanged). Returns
// the organization so the caller can select it. ErrNotPending / ErrExpired
// signal an unusable link; a bad token returns ent's NotFoundError.
func (s *Service) Accept(ctx context.Context, token string, userID uuid.UUID) (*ent.Organization, error) {
	tx, err := s.ent.Tx(ctx)
	if err != nil {
		return nil, err
	}
	inv, err := tx.Invitation.Query().
		Where(invitation.TokenEQ(token)).
		Only(ctx)
	if err != nil {
		return nil, rollback(tx, err)
	}
	if inv.Status != invitation.StatusPending {
		return nil, rollback(tx, ErrNotPending)
	}
	if time.Now().After(inv.ExpiresAt) {
		return nil, rollback(tx, ErrExpired)
	}

	// Create the membership unless the user already belongs to the org.
	exists, err := tx.Membership.Query().
		Where(membership.UserID(userID), membership.OrganizationID(inv.OrganizationID)).
		Exist(ctx)
	if err != nil {
		return nil, rollback(tx, err)
	}
	if !exists {
		if _, err := tx.Membership.Create().
			SetOrganizationID(inv.OrganizationID).
			SetUserID(userID).
			SetRole(membership.Role(inv.Role)).
			Save(ctx); err != nil {
			return nil, rollback(tx, err)
		}
	}

	if _, err := tx.Invitation.UpdateOneID(inv.ID).
		SetStatus(invitation.StatusAccepted).
		SetAcceptedAt(time.Now()).
		Save(ctx); err != nil {
		return nil, rollback(tx, err)
	}

	org, err := tx.Organization.Get(ctx, inv.OrganizationID)
	if err != nil {
		return nil, rollback(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return org, nil
}

// newToken returns a random URL-safe bearer token (32 bytes of entropy).
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("invitations: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// rollback wraps tx.Rollback so a failed mutation surfaces both the original
// error and any rollback failure.
func rollback(tx *ent.Tx, err error) error {
	if rerr := tx.Rollback(); rerr != nil {
		return fmt.Errorf("%w: rollback: %v", err, rerr)
	}
	return err
}
