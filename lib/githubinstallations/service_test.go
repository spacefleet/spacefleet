//go:build integration

package githubinstallations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/githubapp"
	"github.com/spacefleet/spacefleet/lib/testsupport"
)

// fakeAuth is a stub Authenticator: it records the installation it is asked
// about and returns canned values, so the service tests don't reach GitHub.
// repos/failRepos are keyed by GitHub installation id and only exercised by the
// repository-listing tests; the zero value (nil maps) is fine for every other
// test, which never lists repositories.
type fakeAuth struct {
	login     string
	token     string
	repos     map[int64][]githubapp.Repository
	failRepos map[int64]bool
}

func (f fakeAuth) GetInstallation(_ context.Context, _ int64) (githubapp.Installation, error) {
	return githubapp.Installation{Login: f.login, AccountType: "Organization"}, nil
}

func (f fakeAuth) InstallationToken(_ context.Context, _ int64) (string, time.Time, error) {
	return f.token, time.Now().Add(time.Hour), nil
}

func (f fakeAuth) ListRepositories(_ context.Context, installationID int64) ([]githubapp.Repository, error) {
	if f.failRepos[installationID] {
		return nil, errors.New("installation access revoked")
	}
	return f.repos[installationID], nil
}

func newOrg(t *testing.T, client *ent.Client, name string) *ent.Organization {
	t.Helper()
	org, err := client.Organization.Create().SetName(name).Save(context.Background())
	if err != nil {
		t.Fatalf("create org %q: %v", name, err)
	}
	return org
}

// TestLinkVerifiesAndUpserts confirms Link records the account from the
// authenticator, is scoped to its org, and upserts on a repeated callback.
func TestLinkVerifiesAndUpserts(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, fakeAuth{login: "acme", token: "ghs_x"})
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	inst, err := svc.Link(ctx, org.ID, 12345)
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if inst.AccountLogin != "acme" || inst.InstallationID != 12345 {
		t.Errorf("installation = %+v, want acme/12345", inst)
	}

	// A repeated callback for the same installation upserts, not duplicates.
	again, err := svc.Link(ctx, org.ID, 12345)
	if err != nil {
		t.Fatalf("Link (repeat): %v", err)
	}
	if again.ID != inst.ID {
		t.Errorf("repeated Link created a new row (%v != %v)", again.ID, inst.ID)
	}
	all, err := svc.List(ctx, org.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("List returned %d rows, want 1 (upsert)", len(all))
	}

	// A different org cannot see it.
	other := newOrg(t, client, "Other")
	if _, err := svc.Get(ctx, other.ID, inst.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-org Get error = %v, want NotFound", err)
	}
}

// TestInstallationTokenMints confirms the token-mint seam resolves the org's
// record and returns the authenticator's token.
func TestInstallationTokenMints(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, fakeAuth{login: "acme", token: "ghs_minted"})
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	inst, err := svc.Link(ctx, org.ID, 7)
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	tok, err := svc.InstallationToken(ctx, org.ID, inst.ID)
	if err != nil {
		t.Fatalf("InstallationToken: %v", err)
	}
	if tok != "ghs_minted" {
		t.Errorf("token = %q, want ghs_minted", tok)
	}
}

// TestListRepositoriesAggregatesAndSkips confirms ListRepositories gathers
// repositories across all of the org's installations (tagged with the
// installation record), and skips an installation whose listing fails rather
// than failing the whole call.
func TestListRepositoriesAggregatesAndSkips(t *testing.T) {
	client := testsupport.NewEntClient(t)
	auth := fakeAuth{
		login: "acme",
		repos: map[int64][]githubapp.Repository{
			11: {{FullName: "acme/charts", CloneURL: "https://github.com/acme/charts.git"}},
		},
		failRepos: map[int64]bool{22: true},
	}
	svc := NewService(client, auth)
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	good, err := svc.Link(ctx, org.ID, 11)
	if err != nil {
		t.Fatalf("Link good: %v", err)
	}
	if _, err := svc.Link(ctx, org.ID, 22); err != nil {
		t.Fatalf("Link failing: %v", err)
	}

	repos, err := svc.ListRepositories(ctx, org.ID)
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("got %d repos, want 1 (the failing installation is skipped)", len(repos))
	}
	if repos[0].FullName != "acme/charts" {
		t.Errorf("FullName = %q, want acme/charts", repos[0].FullName)
	}
	if repos[0].InstallationID != good.ID {
		t.Errorf("InstallationID = %v, want %v (the record, not GitHub's id)", repos[0].InstallationID, good.ID)
	}
	if repos[0].AccountLogin != "acme" {
		t.Errorf("AccountLogin = %q, want acme", repos[0].AccountLogin)
	}
}

// TestListRepositoriesNoApp confirms the aggregate listing fails clearly when no
// authenticator is wired.
func TestListRepositoriesNoApp(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, nil)
	org := newOrg(t, client, "Acme")
	if _, err := svc.ListRepositories(context.Background(), org.ID); !errors.Is(err, ErrAppNotConfigured) {
		t.Errorf("ListRepositories error = %v, want ErrAppNotConfigured", err)
	}
}

// TestNoAppConfigured confirms Link/InstallationToken fail clearly when no
// authenticator is wired, while read/delete keep working.
func TestNoAppConfigured(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, nil)
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	if _, err := svc.Link(ctx, org.ID, 1); !errors.Is(err, ErrAppNotConfigured) {
		t.Errorf("Link error = %v, want ErrAppNotConfigured", err)
	}
	// List still works (returns empty).
	if list, err := svc.List(ctx, org.ID); err != nil || len(list) != 0 {
		t.Errorf("List = %v, %v; want [], nil", list, err)
	}
}

// TestDeleteInUseRejected confirms an installation attached to a workflow
// component can't be deleted (the FK is ON DELETE RESTRICT → ErrInUse).
func TestDeleteInUseRejected(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, fakeAuth{login: "acme"})
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	inst, err := svc.Link(ctx, org.ID, 99)
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	target, err := client.Cluster.Create().SetOrganizationID(org.ID).SetName("t").SetConnectionMethod("token").Save(ctx)
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	// The installation FK now lives on a component, not the application.
	app, err := client.Application.Create().
		SetOrganizationID(org.ID).
		SetName("web").
		SetTargetNamespace("apps").
		SetTargetClusterID(target.ID).
		SetRunnerClusterID(target.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create application: %v", err)
	}
	if _, err := client.Component.Create().
		SetOrganizationID(org.ID).
		SetApplicationID(app.ID).
		SetName("chart").
		SetType("helm").
		SetGithubInstallationID(inst.ID).
		Save(ctx); err != nil {
		t.Fatalf("create component: %v", err)
	}

	if err := svc.Delete(ctx, org.ID, inst.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("Delete in-use installation error = %v, want ErrInUse", err)
	}
}
