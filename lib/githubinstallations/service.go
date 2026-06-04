// Package githubinstallations holds the GitHub-installation use cases:
// recording an organization's installation of the operator's GitHub App (so it
// can deploy charts from private Git repositories), listing/fetching/deleting
// them, and minting a short-lived installation access token for a rollout.
// Surfaced in the UI under "GitHub". It is a thin wrapper over the ent client
// plus the GitHub App authenticator (lib/githubapp).
//
// Like every org-scoped resource, every query is scoped by organization id —
// that scoping, not the handler's membership check, is the security boundary.
// No secret is stored: a row carries only the numeric installation id and the
// account it is installed on. The access token is minted on demand from the
// operator's App private key and never persisted.
package githubinstallations

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/githubinstallation"
	"github.com/spacefleet/spacefleet/lib/githubapp"
)

// ErrInUse is returned by Delete when the installation is still attached to an
// application: the FK is ON DELETE RESTRICT, so the database refuses the delete.
// The handler maps it to 409.
var ErrInUse = errors.New("github installation is attached to an application")

// ErrAppNotConfigured is returned when an operation needs the GitHub App but the
// operator hasn't configured one (no authenticator wired). The handler maps it
// to 503.
var ErrAppNotConfigured = errors.New("github app is not configured on this deployment")

// Authenticator is the slice of *githubapp.Authenticator this service needs:
// verifying an installation on connect and minting tokens for a rollout. It is
// an interface (and may be nil) so the service is usable without a configured
// GitHub App — operations that need it then return ErrAppNotConfigured.
type Authenticator interface {
	GetInstallation(ctx context.Context, installationID int64) (githubapp.Installation, error)
	InstallationToken(ctx context.Context, installationID int64) (token string, expiresAt time.Time, err error)
}

// Service is a thin wrapper over the ent client plus the GitHub App
// authenticator (which may be nil when no App is configured).
type Service struct {
	ent  *ent.Client
	auth Authenticator
}

// NewService builds the service. auth may be nil (no GitHub App configured);
// Link and InstallationToken then return ErrAppNotConfigured, while read/delete
// keep working on already-recorded installations. Callers must pass an untyped
// nil (not a nil *githubapp.Authenticator) when no App is configured.
func NewService(entClient *ent.Client, auth Authenticator) *Service {
	return &Service{ent: entClient, auth: auth}
}

// List returns the organization's GitHub installations, oldest first.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]*ent.GitHubInstallation, error) {
	return s.ent.GitHubInstallation.Query().
		Where(githubinstallation.OrganizationID(orgID)).
		Order(ent.Asc(githubinstallation.FieldCreatedAt)).
		All(ctx)
}

// Get returns one installation scoped to the organization, or ent's
// NotFoundError.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (*ent.GitHubInstallation, error) {
	return s.ent.GitHubInstallation.Query().
		Where(githubinstallation.OrganizationID(orgID), githubinstallation.ID(id)).
		Only(ctx)
}

// Link records an installation for the organization, verifying it exists for
// this App first (and capturing the account it is installed on). It upserts on
// the (organization_id, installation_id) unique index, so a repeated connect
// callback — GitHub fires both setup_action=install and =update — is idempotent.
func (s *Service) Link(ctx context.Context, orgID uuid.UUID, installationID int64) (*ent.GitHubInstallation, error) {
	if s.auth == nil {
		return nil, ErrAppNotConfigured
	}
	inst, err := s.auth.GetInstallation(ctx, installationID)
	if err != nil {
		return nil, err
	}
	id, err := s.ent.GitHubInstallation.Create().
		SetOrganizationID(orgID).
		SetInstallationID(installationID).
		SetAccountLogin(inst.Login).
		SetAccountType(inst.AccountType).
		OnConflictColumns(githubinstallation.FieldOrganizationID, githubinstallation.FieldInstallationID).
		Update(func(u *ent.GitHubInstallationUpsert) {
			u.SetAccountLogin(inst.Login)
			u.SetAccountType(inst.AccountType)
			u.SetUpdatedAt(time.Now())
		}).
		ID(ctx)
	if err != nil {
		return nil, err
	}
	return s.ent.GitHubInstallation.Get(ctx, id)
}

// Delete removes an installation scoped to the organization. An installation
// still attached to an application fails with ErrInUse (the FK is ON DELETE
// RESTRICT), which the handler maps to 409.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return err
	}
	if err := s.ent.GitHubInstallation.DeleteOne(c).Exec(ctx); err != nil {
		if isForeignKeyViolation(err) {
			return ErrInUse
		}
		return err
	}
	return nil
}

// InstallationToken mints a short-lived access token for the org's installation,
// for the rollout to authenticate a private-Git chart clone. Called late, per
// rollout attempt (so River retries always carry a fresh token); the result
// never reaches an API response.
func (s *Service) InstallationToken(ctx context.Context, orgID, id uuid.UUID) (string, error) {
	if s.auth == nil {
		return "", ErrAppNotConfigured
	}
	inst, err := s.Get(ctx, orgID, id)
	if err != nil {
		return "", err
	}
	token, _, err := s.auth.InstallationToken(ctx, inst.InstallationID)
	if err != nil {
		return "", err
	}
	return token, nil
}

// isForeignKeyViolation reports whether err is a Postgres integrity-constraint
// violation from a referencing row — a RESTRICT (23001) or NO ACTION / plain FK
// (23503) rejection — which for a delete means the row is still referenced.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23001" || pgErr.Code == "23503"
}
