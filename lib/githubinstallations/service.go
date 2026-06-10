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

// ErrInUse is returned by Delete when the installation is still attached to a
// workflow component: the FK is ON DELETE RESTRICT, so the database refuses the
// delete. The handler maps it to 409.
var ErrInUse = errors.New("github installation is attached to a workflow component")

// ErrAppNotConfigured is returned when an operation needs the GitHub App but the
// operator hasn't configured one (no authenticator wired). The handler maps it
// to 503.
var ErrAppNotConfigured = errors.New("github app is not configured on this deployment")

// Authenticator is the slice of *githubapp.Authenticator this service needs:
// verifying the installing user can access an installation on connect and
// minting tokens for a rollout. It is an interface (and may be nil) so the
// service is usable without a configured GitHub App — operations that need it
// then return ErrAppNotConfigured.
type Authenticator interface {
	VerifyUserInstallation(ctx context.Context, code string, installationID int64) (githubapp.Installation, error)
	InstallationToken(ctx context.Context, installationID int64) (token string, expiresAt time.Time, err error)
	ListRepositories(ctx context.Context, installationID int64) ([]githubapp.Repository, error)
}

// Repository is a repository reachable through one of the organization's
// installations, tagged with which installation record it came from so the UI
// can both fill the clone URL and select the matching installation in one step.
type Repository struct {
	// InstallationID is the spacefleet installation record id (not GitHub's
	// numeric id) — the value the component stores in github_installation_id.
	InstallationID uuid.UUID
	AccountLogin   string
	githubapp.Repository
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

// ListRepositories returns every repository reachable across all of the
// organization's installations, each tagged with its installation record so the
// caller can select the matching installation. It is resilient per installation:
// if one fails (its access was revoked, GitHub is unreachable, a token can't be
// minted), that installation is skipped rather than failing the whole list.
//
// Known limitation: installations are queried sequentially and the result is not
// cached, so an organization with many installations incurs serial GitHub
// round-trips on each call.
func (s *Service) ListRepositories(ctx context.Context, orgID uuid.UUID) ([]Repository, error) {
	if s.auth == nil {
		return nil, ErrAppNotConfigured
	}
	installs, err := s.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var repos []Repository
	for _, inst := range installs {
		ghRepos, err := s.auth.ListRepositories(ctx, inst.InstallationID)
		if err != nil {
			// Skip an installation we can't read rather than failing the whole
			// picker — others may still be reachable.
			continue
		}
		for _, r := range ghRepos {
			repos = append(repos, Repository{
				InstallationID: inst.ID,
				AccountLogin:   inst.AccountLogin,
				Repository:     r,
			})
		}
	}
	return repos, nil
}

// Get returns one installation scoped to the organization, or ent's
// NotFoundError.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (*ent.GitHubInstallation, error) {
	return s.ent.GitHubInstallation.Query().
		Where(githubinstallation.OrganizationID(orgID), githubinstallation.ID(id)).
		Only(ctx)
}

// Link records an installation for the organization, first proving the user
// completing the callback can actually access it: code (the OAuth authorization
// code from GitHub's redirect) is exchanged for a user token and the
// installation must appear in that user's installation list (see
// githubapp.VerifyUserInstallation — an existence check via the App JWT would
// let anyone link someone else's installation). The verified lookup also
// captures the account the App is installed on. It upserts on the
// (organization_id, installation_id) unique index, so a repeated connect
// callback is idempotent.
func (s *Service) Link(ctx context.Context, orgID uuid.UUID, installationID int64, code string) (*ent.GitHubInstallation, error) {
	if s.auth == nil {
		return nil, ErrAppNotConfigured
	}
	inst, err := s.auth.VerifyUserInstallation(ctx, code, installationID)
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
// still attached to a workflow component fails with ErrInUse (the FK is ON
// DELETE RESTRICT), which the handler maps to 409.
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
