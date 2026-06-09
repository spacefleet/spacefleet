//go:build integration

package applicationgroups

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/lib/testsupport"
)

func newOrg(t *testing.T, client *ent.Client, name string) *ent.Organization {
	t.Helper()
	org, err := client.Organization.Create().SetName(name).Save(context.Background())
	if err != nil {
		t.Fatalf("create org %q: %v", name, err)
	}
	return org
}

func TestCreateListAndOrgScoped(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	other := newOrg(t, client, "Other")

	g, err := svc.Create(ctx, org.ID, "Backend")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The owning org sees it; another org does not.
	list, err := svc.List(ctx, org.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != g.ID {
		t.Errorf("List = %+v, want the created group", list)
	}
	otherList, err := svc.List(ctx, other.ID)
	if err != nil {
		t.Fatalf("List other: %v", err)
	}
	if len(otherList) != 0 {
		t.Errorf("other org List = %+v, want empty", otherList)
	}
	if _, err := svc.Get(ctx, other.ID, g.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-org Get error = %v, want NotFound", err)
	}
}

func TestCreateEmptyNameRejected(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	if _, err := svc.Create(ctx, org.ID, ""); !IsValidation(err) {
		t.Fatalf("Create empty name error = %v, want ValidationError", err)
	}
}

func TestCreateDuplicateNameConflicts(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	if _, err := svc.Create(ctx, org.ID, "Backend"); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	// Same name in the same org violates the unique index.
	if _, err := svc.Create(ctx, org.ID, "Backend"); !ent.IsConstraintError(err) {
		t.Fatalf("duplicate Create error = %v, want ConstraintError", err)
	}
	// The same name in a different org is fine.
	other := newOrg(t, client, "Other")
	if _, err := svc.Create(ctx, other.ID, "Backend"); err != nil {
		t.Fatalf("same name other org: %v", err)
	}
}

func TestUpdateRenames(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	g, err := svc.Create(ctx, org.ID, "Backend")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	newName := "Services"
	updated, err := svc.Update(ctx, org.ID, g.ID, UpdateParams{Name: &newName})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "Services" {
		t.Errorf("name = %q, want Services", updated.Name)
	}

	empty := ""
	if _, err := svc.Update(ctx, org.ID, g.ID, UpdateParams{Name: &empty}); !IsValidation(err) {
		t.Errorf("empty rename error = %v, want ValidationError", err)
	}
}

func TestDeleteUngroupsApplications(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	// A cluster pair to satisfy the application's required FKs.
	c, err := client.Cluster.Create().
		SetOrganizationID(org.ID).
		SetName("c").
		SetConnectionMethod(cluster.ConnectionMethodToken).
		Save(ctx)
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	g, err := svc.Create(ctx, org.ID, "Backend")
	if err != nil {
		t.Fatalf("Create group: %v", err)
	}
	app, err := client.Application.Create().
		SetOrganizationID(org.ID).
		SetName("web").
		SetRunnerClusterID(c.ID).
		SetGroupID(g.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	// Deleting the group must not delete the app — it falls back to the root
	// (group_id set null by the ON DELETE SET NULL FK).
	if err := svc.Delete(ctx, org.ID, g.ID); err != nil {
		t.Fatalf("Delete group: %v", err)
	}
	reloaded, err := client.Application.Get(ctx, app.ID)
	if err != nil {
		t.Fatalf("reload app: %v", err)
	}
	if reloaded.GroupID != uuid.Nil {
		t.Errorf("app GroupID after group delete = %v, want nil (ungrouped)", reloaded.GroupID)
	}
}

func TestDeleteOrgScoped(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	other := newOrg(t, client, "Other")
	g, err := svc.Create(ctx, org.ID, "Backend")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Delete(ctx, other.ID, g.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-org Delete error = %v, want NotFound", err)
	}
	if err := svc.Delete(ctx, org.ID, g.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
