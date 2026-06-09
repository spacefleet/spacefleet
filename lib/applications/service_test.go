//go:build integration

package applications

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

func newCluster(t *testing.T, client *ent.Client, orgID uuid.UUID, name string, method cluster.ConnectionMethod, tektonEnabled bool) *ent.Cluster {
	t.Helper()
	ctx := context.Background()
	c, err := client.Cluster.Create().
		SetOrganizationID(orgID).
		SetName(name).
		SetConnectionMethod(method).
		Save(ctx)
	if err != nil {
		t.Fatalf("create cluster %q: %v", name, err)
	}
	if tektonEnabled {
		if _, err := client.TektonInstallation.Create().SetClusterID(c.ID).SetEnabled(true).Save(ctx); err != nil {
			t.Fatalf("enable tekton on %q: %v", name, err)
		}
	}
	return c
}

func appParams(target, runner uuid.UUID) CreateParams {
	return CreateParams{
		Name:            "web",
		TargetNamespace: "apps",
		TargetClusterID: target,
		RunnerClusterID: runner,
	}
}

func TestCreateValidAndOrgScoped(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)

	app, err := svc.Create(ctx, org.ID, appParams(target.ID, runner.ID))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if app.Imported {
		t.Errorf("a created app must not be imported")
	}

	// A different org cannot see it.
	other := newOrg(t, client, "Other")
	if _, err := svc.Get(ctx, other.ID, app.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-org Get error = %v, want NotFound", err)
	}
	// The owning org can.
	if _, err := svc.Get(ctx, org.ID, app.ID); err != nil {
		t.Errorf("same-org Get: %v", err)
	}
}

func TestCreateRunnerNotJobRunner(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, false) // tekton not enabled

	_, err := svc.Create(ctx, org.ID, appParams(target.ID, runner.ID))
	if !IsValidation(err) {
		t.Fatalf("Create error = %v, want ValidationError", err)
	}
}

func TestCreateInClusterRequiresSameRunner(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "in-cluster-target", cluster.ConnectionMethodInCluster, false)
	runner := newCluster(t, client, org.ID, "other-runner", cluster.ConnectionMethodToken, true)

	// in_cluster target + different runner → rejected.
	_, err := svc.Create(ctx, org.ID, appParams(target.ID, runner.ID))
	if !IsValidation(err) {
		t.Fatalf("mismatched runner error = %v, want ValidationError", err)
	}

	// in_cluster target where the target is itself the (tekton-enabled) runner → ok.
	selfRunner := newCluster(t, client, org.ID, "self", cluster.ConnectionMethodInCluster, true)
	app, err := svc.Create(ctx, org.ID, appParams(selfRunner.ID, selfRunner.ID))
	if err != nil {
		t.Fatalf("in_cluster self-runner Create: %v", err)
	}
	if app.TargetClusterID != app.RunnerClusterID {
		t.Errorf("expected target == runner")
	}
}

func TestCreateMissingNamespace(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)

	p := appParams(target.ID, runner.ID)
	p.TargetNamespace = ""
	if _, err := svc.Create(ctx, org.ID, p); !IsValidation(err) {
		t.Fatalf("error = %v, want ValidationError for missing namespace", err)
	}
}

func TestCreateTargetClusterFromAnotherOrgRejected(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	other := newOrg(t, client, "Other")
	foreignTarget := newCluster(t, client, other.ID, "foreign", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)

	_, err := svc.Create(ctx, org.ID, appParams(foreignTarget.ID, runner.ID))
	if !IsValidation(err) {
		t.Fatalf("error = %v, want ValidationError for cross-org target", err)
	}
}

func TestUpdateMutableFields(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)
	app, err := svc.Create(ctx, org.ID, appParams(target.ID, runner.ID))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	newName := "web2"
	newNS := "prod"
	updated, err := svc.Update(ctx, org.ID, app.ID, UpdateParams{Name: &newName, TargetNamespace: &newNS})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "web2" || updated.TargetNamespace != "prod" {
		t.Errorf("update not persisted: %+v", updated)
	}

	// An empty namespace is rejected.
	empty := ""
	if _, err := svc.Update(ctx, org.ID, app.ID, UpdateParams{TargetNamespace: &empty}); !IsValidation(err) {
		t.Errorf("empty namespace update error = %v, want ValidationError", err)
	}
}

func TestAdoptCreatesImported(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)

	app, err := svc.Adopt(ctx, org.ID, appParams(target.ID, runner.ID))
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if !app.Imported {
		t.Errorf("adopted app must be imported")
	}
}

func TestAdoptRejectsInvalidLikeCreate(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, false) // tekton not enabled

	if _, err := svc.Adopt(ctx, org.ID, appParams(target.ID, runner.ID)); !IsValidation(err) {
		t.Fatalf("Adopt error = %v, want ValidationError", err)
	}
}

func TestDeleteOrgScoped(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	other := newOrg(t, client, "Other")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)
	app, err := svc.Create(ctx, org.ID, appParams(target.ID, runner.ID))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Another org can't delete it.
	if err := svc.Delete(ctx, other.ID, app.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-org Delete error = %v, want NotFound", err)
	}
	if err := svc.Delete(ctx, org.ID, app.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, org.ID, app.ID); !ent.IsNotFound(err) {
		t.Errorf("after delete Get error = %v, want NotFound", err)
	}
}

func newGroup(t *testing.T, client *ent.Client, orgID uuid.UUID, name string) *ent.ApplicationGroup {
	t.Helper()
	g, err := client.ApplicationGroup.Create().SetOrganizationID(orgID).SetName(name).Save(context.Background())
	if err != nil {
		t.Fatalf("create group %q: %v", name, err)
	}
	return g
}

func TestSetGroupMovesAndClears(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)
	app, err := svc.Create(ctx, org.ID, appParams(target.ID, runner.ID))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if app.GroupID != uuid.Nil {
		t.Fatalf("new app should be ungrouped, got group %v", app.GroupID)
	}
	group := newGroup(t, client, org.ID, "Backend")

	// Move into the group.
	moved, err := svc.SetGroup(ctx, org.ID, app.ID, &group.ID)
	if err != nil {
		t.Fatalf("SetGroup into group: %v", err)
	}
	if moved.GroupID != group.ID {
		t.Errorf("GroupID = %v, want %v", moved.GroupID, group.ID)
	}

	// Move back to the root.
	cleared, err := svc.SetGroup(ctx, org.ID, app.ID, nil)
	if err != nil {
		t.Fatalf("SetGroup to root: %v", err)
	}
	if cleared.GroupID != uuid.Nil {
		t.Errorf("GroupID after clear = %v, want nil", cleared.GroupID)
	}
}

func TestSetGroupRejectsForeignGroup(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	other := newOrg(t, client, "Other")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)
	app, err := svc.Create(ctx, org.ID, appParams(target.ID, runner.ID))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	foreign := newGroup(t, client, other.ID, "Theirs")

	if _, err := svc.SetGroup(ctx, org.ID, app.ID, &foreign.ID); !IsValidation(err) {
		t.Fatalf("SetGroup with foreign group error = %v, want ValidationError", err)
	}
}

func TestCreateWithGroup(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)
	group := newGroup(t, client, org.ID, "Backend")

	p := appParams(target.ID, runner.ID)
	p.GroupID = &group.ID
	app, err := svc.Create(ctx, org.ID, p)
	if err != nil {
		t.Fatalf("Create with group: %v", err)
	}
	if app.GroupID != group.ID {
		t.Errorf("GroupID = %v, want %v", app.GroupID, group.ID)
	}

	// A foreign group is rejected at create time too.
	other := newOrg(t, client, "Other")
	foreign := newGroup(t, client, other.ID, "Theirs")
	p2 := appParams(target.ID, runner.ID)
	p2.Name = "web2"
	p2.GroupID = &foreign.ID
	if _, err := svc.Create(ctx, org.ID, p2); !IsValidation(err) {
		t.Fatalf("Create with foreign group error = %v, want ValidationError", err)
	}
}
