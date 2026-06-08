//go:build integration

package variables

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/cluster"
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

// newApp creates an application (with the clusters its FKs require) and returns
// it, so variables can hang off a real application row.
func newApp(t *testing.T, client *ent.Client, orgID uuid.UUID, name string) *ent.Application {
	t.Helper()
	ctx := context.Background()
	mkCluster := func(n string) *ent.Cluster {
		c, err := client.Cluster.Create().
			SetOrganizationID(orgID).
			SetName(n).
			SetConnectionMethod(cluster.ConnectionMethodToken).
			Save(ctx)
		if err != nil {
			t.Fatalf("create cluster %q: %v", n, err)
		}
		return c
	}
	app, err := client.Application.Create().
		SetOrganizationID(orgID).
		SetName(name).
		SetTargetNamespace("apps").
		SetTargetClusterID(mkCluster(name + "-target").ID).
		SetRunnerClusterID(mkCluster(name + "-runner").ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create app %q: %v", name, err)
	}
	return app
}

func newComponent(t *testing.T, client *ent.Client, orgID, appID uuid.UUID, name string) *ent.Component {
	t.Helper()
	c, err := client.Component.Create().
		SetOrganizationID(orgID).
		SetApplicationID(appID).
		SetName(name).
		SetType("helm").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create component %q: %v", name, err)
	}
	return c
}

// TestSensitiveSealedAtRest confirms a sensitive variable is sealed (its value
// not stored in plaintext) while a non-secret one stores its value plainly.
func TestSensitiveSealedAtRest(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")

	sec, err := svc.Create(ctx, org.ID, app.ID, nil, CreateParams{Name: "API_KEY", Sensitive: true, Value: "sk-live-123"})
	if err != nil {
		t.Fatalf("Create sensitive: %v", err)
	}
	if sec.Value != "" {
		t.Errorf("sensitive variable stored plaintext value %q, want empty", sec.Value)
	}
	if sec.EncryptedValue == nil || string(*sec.EncryptedValue) == "sk-live-123" {
		t.Errorf("sensitive value was not sealed at rest")
	}

	plain, err := svc.Create(ctx, org.ID, app.ID, nil, CreateParams{Name: "LOG_LEVEL", Sensitive: false, Value: "debug"})
	if err != nil {
		t.Fatalf("Create non-secret: %v", err)
	}
	if plain.Value != "debug" {
		t.Errorf("non-secret value = %q, want debug", plain.Value)
	}
}

// TestResolveEnvOverride confirms component-level variables override app-level
// ones of the same name (value and sensitivity), and that sensitive values are
// decrypted into the secret map while non-secret ones land in plain.
func TestResolveEnvOverride(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	comp := newComponent(t, client, org.ID, app.ID, "api")

	// App-level: FOO (plain), TOKEN (sensitive), REGION (plain).
	mustCreate(t, svc, org.ID, app.ID, nil, CreateParams{Name: "FOO", Value: "app-foo"})
	mustCreate(t, svc, org.ID, app.ID, nil, CreateParams{Name: "TOKEN", Sensitive: true, Value: "app-token"})
	mustCreate(t, svc, org.ID, app.ID, nil, CreateParams{Name: "REGION", Value: "us-east-1"})
	// Component-level: FOO overrides (plain), TOKEN overrides flipping to plain.
	cid := comp.ID
	mustCreate(t, svc, org.ID, app.ID, &cid, CreateParams{Name: "FOO", Value: "comp-foo"})
	mustCreate(t, svc, org.ID, app.ID, &cid, CreateParams{Name: "TOKEN", Value: "comp-token-plain"})

	plain, secret, err := svc.ResolveEnv(ctx, org.ID, app.ID, comp.ID)
	if err != nil {
		t.Fatalf("ResolveEnv: %v", err)
	}
	if plain["FOO"] != "comp-foo" {
		t.Errorf("FOO = %q, want comp-foo (component override)", plain["FOO"])
	}
	if plain["REGION"] != "us-east-1" {
		t.Errorf("REGION = %q, want app-level us-east-1", plain["REGION"])
	}
	// TOKEN was sensitive at app level but the component overrides it as plain.
	if plain["TOKEN"] != "comp-token-plain" {
		t.Errorf("TOKEN plain = %q, want comp-token-plain", plain["TOKEN"])
	}
	if _, ok := secret["TOKEN"]; ok {
		t.Errorf("TOKEN should not be in secret map after a plain component override")
	}

	// A component with no overrides sees only the app-level set (TOKEN sealed).
	other := newComponent(t, client, org.ID, app.ID, "worker")
	plain2, secret2, err := svc.ResolveEnv(ctx, org.ID, app.ID, other.ID)
	if err != nil {
		t.Fatalf("ResolveEnv other: %v", err)
	}
	if plain2["FOO"] != "app-foo" {
		t.Errorf("other FOO = %q, want app-foo", plain2["FOO"])
	}
	if secret2["TOKEN"] != "app-token" {
		t.Errorf("other TOKEN secret = %q, want decrypted app-token", secret2["TOKEN"])
	}
	if _, ok := plain2["TOKEN"]; ok {
		t.Errorf("sensitive TOKEN leaked into plain map")
	}
}

// TestNameValidationAndUniqueness confirms a bad name is rejected and a duplicate
// name in the same scope is rejected, while the same name in different scopes is
// allowed.
func TestNameValidationAndUniqueness(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	comp := newComponent(t, client, org.ID, app.ID, "api")

	if _, err := svc.Create(ctx, org.ID, app.ID, nil, CreateParams{Name: "1bad", Value: "x"}); !IsValidation(err) {
		t.Errorf("bad name error = %v, want ValidationError", err)
	}
	mustCreate(t, svc, org.ID, app.ID, nil, CreateParams{Name: "FOO", Value: "x"})
	if _, err := svc.Create(ctx, org.ID, app.ID, nil, CreateParams{Name: "FOO", Value: "y"}); !IsValidation(err) {
		t.Errorf("duplicate app-level name error = %v, want ValidationError", err)
	}
	// Same name at the component level is a different scope — allowed.
	cid := comp.ID
	if _, err := svc.Create(ctx, org.ID, app.ID, &cid, CreateParams{Name: "FOO", Value: "z"}); err != nil {
		t.Errorf("component-level FOO should be allowed alongside app-level FOO: %v", err)
	}

	// A sensitive variable requires a non-empty value.
	if _, err := svc.Create(ctx, org.ID, app.ID, nil, CreateParams{Name: "EMPTY", Sensitive: true, Value: ""}); !IsValidation(err) {
		t.Errorf("empty sensitive value error = %v, want ValidationError", err)
	}
	// A component-level variable on a missing component is rejected.
	missing := uuid.New()
	if _, err := svc.Create(ctx, org.ID, app.ID, &missing, CreateParams{Name: "X", Value: "1"}); !IsValidation(err) {
		t.Errorf("missing-component error = %v, want ValidationError", err)
	}
}

// TestUpdateKeepsSealedValueWhenOmitted confirms omitting the value on update
// leaves a sensitive value intact, while supplying one re-seals it.
func TestUpdateKeepsSealedValueWhenOmitted(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")

	v := mustCreate(t, svc, org.ID, app.ID, nil, CreateParams{Name: "TOKEN", Sensitive: true, Value: "old"})

	// Rename only (no value) — the sealed value survives.
	newName := "TOKEN_RENAMED"
	if _, err := svc.Update(ctx, org.ID, app.ID, nil, v.ID, UpdateParams{Name: &newName}); err != nil {
		t.Fatalf("Update rename: %v", err)
	}
	_, secret, err := svc.ResolveEnv(ctx, org.ID, app.ID, uuid.Nil)
	if err != nil {
		t.Fatalf("ResolveEnv: %v", err)
	}
	if secret["TOKEN_RENAMED"] != "old" {
		t.Errorf("value after rename = %q, want old (unchanged)", secret["TOKEN_RENAMED"])
	}

	// Replace the value — re-sealed.
	nv := "new"
	if _, err := svc.Update(ctx, org.ID, app.ID, nil, v.ID, UpdateParams{Value: &nv}); err != nil {
		t.Fatalf("Update value: %v", err)
	}
	_, secret, err = svc.ResolveEnv(ctx, org.ID, app.ID, uuid.Nil)
	if err != nil {
		t.Fatalf("ResolveEnv after replace: %v", err)
	}
	if secret["TOKEN_RENAMED"] != "new" {
		t.Errorf("value after replace = %q, want new", secret["TOKEN_RENAMED"])
	}
}

// TestCrossOrgIsolation confirms variables are scoped to their org/app.
func TestCrossOrgIsolation(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	v := mustCreate(t, svc, org.ID, app.ID, nil, CreateParams{Name: "FOO", Value: "x"})

	other := newOrg(t, client, "Other")
	if err := svc.Delete(ctx, other.ID, app.ID, nil, v.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-org Delete error = %v, want NotFound", err)
	}
}

// TestCreateWithoutKeyDisabled confirms a sensitive variable can't be stored
// without an encryption key.
func TestCreateWithoutKeyDisabled(t *testing.T) {
	client := testsupport.NewEntClient(t)
	disabled, _ := secrets.NewSealer("")
	svc := NewService(client, disabled)
	ctx := context.Background()
	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	if _, err := svc.Create(ctx, org.ID, app.ID, nil, CreateParams{Name: "S", Sensitive: true, Value: "x"}); !errors.Is(err, secrets.ErrDisabled) {
		t.Errorf("Create sensitive without key error = %v, want ErrDisabled", err)
	}
}

func mustCreate(t *testing.T, svc *Service, orgID, appID uuid.UUID, componentID *uuid.UUID, p CreateParams) *ent.Variable {
	t.Helper()
	v, err := svc.Create(context.Background(), orgID, appID, componentID, p)
	if err != nil {
		t.Fatalf("Create %q: %v", p.Name, err)
	}
	return v
}
