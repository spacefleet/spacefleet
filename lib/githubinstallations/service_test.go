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
type fakeAuth struct {
	login string
	token string
}

func (f fakeAuth) GetInstallation(_ context.Context, _ int64) (githubapp.Installation, error) {
	return githubapp.Installation{Login: f.login, AccountType: "Organization"}, nil
}

func (f fakeAuth) InstallationToken(_ context.Context, _ int64) (string, time.Time, error) {
	return f.token, time.Now().Add(time.Hour), nil
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
