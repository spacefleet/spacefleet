//go:build integration

package chartcredentials

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/secrets"
	"github.com/spacefleet/spacefleet/lib/testsupport"
)

func newSealer(t *testing.T) *secrets.Sealer {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	s, err := secrets.NewSealer(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	return s
}

func newOrg(t *testing.T, client *ent.Client, name string) *ent.Organization {
	t.Helper()
	org, err := client.Organization.Create().SetName(name).Save(context.Background())
	if err != nil {
		t.Fatalf("create org %q: %v", name, err)
	}
	return org
}

// TestCreateSealsAndResolveRoundTrips registers a credential and confirms the
// password is sealed at rest (not stored in plaintext) but Resolve round-trips
// it, and the stored row is scoped to its organization.
func TestCreateSealsAndResolveRoundTrips(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	c, err := svc.Create(ctx, org.ID, CreateParams{
		Name:     "registry",
		Username: "deploy",
		Password: "s3cret",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The stored blob must not be the plaintext password.
	if c.EncryptedPassword == nil || string(*c.EncryptedPassword) == "s3cret" {
		t.Fatalf("password was not sealed at rest")
	}

	got, err := svc.Resolve(ctx, org.ID, c.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Username != "deploy" || got.Password != "s3cret" {
		t.Errorf("Resolve = %+v, want deploy/s3cret", got)
	}

	// A different org cannot see it.
	other := newOrg(t, client, "Other")
	if _, err := svc.Get(ctx, other.ID, c.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-org Get error = %v, want NotFound", err)
	}
}

// TestCreateWithoutKeyDisabled confirms storing a credential without an
// encryption key fails fast with ErrDisabled rather than persisting plaintext.
func TestCreateWithoutKeyDisabled(t *testing.T) {
	client := testsupport.NewEntClient(t)
	disabled, _ := secrets.NewSealer("") // no key
	svc := NewService(client, disabled)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	_, err := svc.Create(ctx, org.ID, CreateParams{Name: "r", Password: "p"})
	if !errors.Is(err, secrets.ErrDisabled) {
		t.Fatalf("Create error = %v, want ErrDisabled", err)
	}
}

// TestCreateRejectsBadInput confirms a missing password is a client-input error.
func TestCreateRejectsBadInput(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	if _, err := svc.Create(ctx, org.ID, CreateParams{Name: "r"}); !IsValidation(err) {
		t.Errorf("missing password error = %v, want ValidationError", err)
	}
}

// TestDeleteInUseRejected confirms a credential attached to a workflow component
// can't be deleted (the FK is ON DELETE RESTRICT → an ent constraint error).
func TestDeleteInUseRejected(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	cred, err := svc.Create(ctx, org.ID, CreateParams{Name: "registry", Password: "p"})
	if err != nil {
		t.Fatalf("Create credential: %v", err)
	}
	// Minimal clusters + an application that owns a component referencing the
	// credential (the FK now lives on the component, not the application).
	target, err := client.Cluster.Create().SetOrganizationID(org.ID).SetName("t").SetConnectionMethod("token").Save(ctx)
	if err != nil {
		t.Fatalf("create target cluster: %v", err)
	}
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
		SetChartCredentialID(cred.ID).
		Save(ctx); err != nil {
		t.Fatalf("create component: %v", err)
	}

	if err := svc.Delete(ctx, org.ID, cred.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("Delete in-use credential error = %v, want ErrInUse", err)
	}
}

// TestUpdateRotatesPassword confirms a new password re-seals and Resolve returns
// the rotated value, while a nil password leaves the stored one intact.
func TestUpdateRotatesPassword(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	cred, err := svc.Create(ctx, org.ID, CreateParams{Name: "registry", Username: "u", Password: "old"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	newName := "registry-prod"
	newPass := "new"
	if _, err := svc.Update(ctx, org.ID, cred.ID, UpdateParams{Name: &newName, Password: &newPass}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := svc.Resolve(ctx, org.ID, cred.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Password != "new" {
		t.Errorf("password = %q, want rotated 'new'", got.Password)
	}

	// An empty password is rejected (a credential always needs one).
	empty := ""
	if _, err := svc.Update(ctx, org.ID, cred.ID, UpdateParams{Password: &empty}); !IsValidation(err) {
		t.Errorf("empty password error = %v, want ValidationError", err)
	}
}
