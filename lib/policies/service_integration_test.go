//go:build integration

package policies

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/lib/testsupport"
)

const valid = "package spacefleet\n\ndeny contains msg if {\n\tinput.plan.destroy > 0\n\tmsg := \"no destroys\"\n}\n"

func TestPolicies(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()
	org, err := client.Organization.Create().SetName("Acme").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other, err := client.Organization.Create().SetName("Other").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := client.Cluster.Create().SetOrganizationID(org.ID).SetName("r").SetConnectionMethod(cluster.ConnectionMethodToken).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app, err := client.Application.Create().SetOrganizationID(org.ID).SetName("web").SetRunnerClusterID(runner.ID).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Create(ctx, org.ID, CreateParams{Name: "bad", Rego: "package other\n"}); !IsValidation(err) {
		t.Errorf("wrong package: err = %v, want validation", err)
	}
	if _, err := svc.Create(ctx, org.ID, CreateParams{Name: "bad", Rego: valid, Enforcement: "audit"}); !IsValidation(err) {
		t.Errorf("bad enforcement: err = %v", err)
	}
	if _, err := svc.Create(ctx, org.ID, CreateParams{Name: "bad", Rego: valid, ApplicationID: uuid.New()}); !IsValidation(err) {
		t.Errorf("unknown app: err = %v", err)
	}
	p, err := svc.Create(ctx, org.ID, CreateParams{Name: "no-destroys", Rego: valid, ApplicationID: app.ID})
	if err != nil || p.Enforcement != "block" || !p.Enabled || p.ApplicationID != app.ID {
		t.Fatalf("create = %+v err=%v", p, err)
	}
	if _, err := svc.Create(ctx, org.ID, CreateParams{Name: "no-destroys", Rego: valid}); !ent.IsConstraintError(err) {
		t.Errorf("duplicate: err = %v", err)
	}
	if _, err := svc.Get(ctx, other.ID, p.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-org get: err = %v", err)
	}
	broken, warn, off, nilApp := "package spacefleet\n\ndeny contains msg if {\n", "warn", false, uuid.Nil
	if _, err := svc.Update(ctx, org.ID, p.ID, UpdateParams{Rego: &broken}); !IsValidation(err) {
		t.Errorf("broken rego: err = %v", err)
	}
	p, err = svc.Update(ctx, org.ID, p.ID, UpdateParams{Enforcement: &warn, Enabled: &off, ApplicationID: &nilApp})
	if err != nil || p.Enforcement != "warn" || p.Enabled || p.ApplicationID != uuid.Nil {
		t.Errorf("update = %+v err=%v", p, err)
	}
	if list, err := svc.List(ctx, org.ID); err != nil || len(list) != 1 {
		t.Errorf("list = %d err=%v", len(list), err)
	}
	if err := svc.Delete(ctx, other.ID, p.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-org delete: err = %v", err)
	}
	if err := svc.Delete(ctx, org.ID, p.ID); err != nil {
		t.Errorf("delete: %v", err)
	}
}
