//go:build integration

package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/lib/applications"
	"github.com/spacefleet/spacefleet/lib/clusters"
	"github.com/spacefleet/spacefleet/lib/githubapp"
	"github.com/spacefleet/spacefleet/lib/githubinstallations"
	"github.com/spacefleet/spacefleet/lib/organizations"
	"github.com/spacefleet/spacefleet/lib/testsupport"
	"github.com/spacefleet/spacefleet/lib/users"
)

// This file holds the DB-backed half of the handler test harness: it builds the
// real account/resource services over a freshly-migrated Postgres (so the
// resolve/authorize preamble runs for real) plus the fixtures the cross-org
// guard and snapshot happy-path tests need. It is integration-tagged because it
// needs a database; the tag-free harness in harness_test.go drives the nil
// service / pre-stream error paths in the plain `go test` pass.

// fakeGitHubAuth is a stub githubinstallations.Authenticator that never reaches
// GitHub: GetInstallation returns a canned account, so Link succeeds in the
// happy-path test without a live App. (Mirrors the service-test stub.)
type fakeGitHubAuth struct{ login string }

func (f fakeGitHubAuth) GetInstallation(_ context.Context, _ int64) (githubapp.Installation, error) {
	return githubapp.Installation{Login: f.login, AccountType: "Organization"}, nil
}

func (f fakeGitHubAuth) InstallationToken(_ context.Context, _ int64) (string, time.Time, error) {
	return "ghs_test", time.Now().Add(time.Hour), nil
}

func (f fakeGitHubAuth) ListRepositories(_ context.Context, _ int64) ([]githubapp.Repository, error) {
	return []githubapp.Repository{
		{FullName: f.login + "/charts", CloneURL: "https://github.com/" + f.login + "/charts.git", Private: true, DefaultBranch: "main"},
	}, nil
}

// testSecretKey is a valid base64 32-byte key used to sign/verify connect state
// (the same key class as the credential sealer). Stable so a state signed in a
// test verifies under the same harness key.
const testSecretKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// harness is a DB-backed handler-test fixture: a built HTTP tree plus the ent
// client and services behind it, so a test can both drive HTTP and seed rows.
type harness struct {
	t       *testing.T
	client  *ent.Client
	handler http.Handler
}

// newHarness builds real services over a fresh database and wires the handler.
// github is the GitHub App authenticator (pass nil for no App). The secret key
// is always set so the connect-state sign/verify path is exercised.
func newHarness(t *testing.T, github githubinstallations.Authenticator) *harness {
	t.Helper()
	client := testsupport.NewEntClient(t)
	deps := ServerDeps{
		Users:        users.NewService(client),
		Orgs:         organizations.NewService(client),
		Applications: applications.NewService(client),
		// A real clusters service (nil sealer) so the log-stream pre-checks that
		// gate before reaching the cluster (stale_run) run; the happy paths here
		// never actually reach a live cluster.
		Clusters:            clusters.NewService(client, nil),
		GitHubInstallations: githubinstallations.NewService(client, github),
		SecretKey:           testSecretKey,
		GitHubAppSlug:       "spacefleet-test",
	}
	return &harness{t: t, client: client, handler: newTestHandler(deps)}
}

// member provisions a user (whose OIDC subject equals the bearer token the
// FakeVerifier will hand back) and an org with that user at the given role, and
// returns the bearer token and org id to drive requests with.
func (h *harness) member(token string, role membership.Role) (string, uuid.UUID) {
	h.t.Helper()
	ctx := context.Background()
	// The FakeVerifier maps a bearer verbatim to the OIDC subject, so provision
	// the user under that subject; then the handler's currentUser resolves to it.
	u, err := h.client.User.Create().SetOidcSubject(token).SetEmail(token + "@test.local").Save(ctx)
	if err != nil {
		h.t.Fatalf("create user: %v", err)
	}
	org, err := h.client.Organization.Create().SetName("Org-" + token).Save(ctx)
	if err != nil {
		h.t.Fatalf("create org: %v", err)
	}
	if _, err := h.client.Membership.Create().
		SetOrganizationID(org.ID).
		SetUserID(u.ID).
		SetRole(role).
		Save(ctx); err != nil {
		h.t.Fatalf("create membership: %v", err)
	}
	return token, org.ID
}

// newOrgID creates a bare organization (no membership) and returns its id — used
// to mint connect state for an org the caller does NOT belong to.
func (h *harness) newOrgID() uuid.UUID {
	h.t.Helper()
	org, err := h.client.Organization.Create().SetName("Other-" + uuid.NewString()).Save(context.Background())
	if err != nil {
		h.t.Fatalf("create org: %v", err)
	}
	return org.ID
}

// signState signs connect state for org under the harness's secret key.
func (h *harness) signState(org uuid.UUID) string {
	h.t.Helper()
	state, err := githubapp.SignState(testSecretKey, org)
	if err != nil {
		h.t.Fatalf("sign state: %v", err)
	}
	return state
}
