//go:build integration

package applications

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/application"
	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/testsupport"
)

// stubConns satisfies ConnResolver; the validation/org-scoping tests never reach
// a rollout, so it only needs to exist.
type stubConns struct{}

func (stubConns) ConnForTekton(context.Context, uuid.UUID, uuid.UUID) (k8s.Connection, error) {
	return k8s.Connection{}, nil
}

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

func httpRepoParams(target, runner uuid.UUID) CreateParams {
	return CreateParams{
		Name:            "web",
		ChartSource:     helm.SourceHTTPRepo,
		Config:          map[string]string{helm.ConfigRepoURL: "https://charts.example.com", helm.ConfigChart: "nginx"},
		TargetNamespace: "apps",
		TargetClusterID: target,
		RunnerClusterID: runner,
	}
}

func TestCreateValidAndOrgScoped(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{})
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)

	app, err := svc.Create(ctx, org.ID, httpRepoParams(target.ID, runner.ID))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if app.Status != application.StatusPending {
		t.Errorf("status = %q, want pending", app.Status)
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
	svc := NewService(client, stubConns{})
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, false) // tekton not enabled

	_, err := svc.Create(ctx, org.ID, httpRepoParams(target.ID, runner.ID))
	if !IsValidation(err) {
		t.Fatalf("Create error = %v, want ValidationError", err)
	}
}

func TestCreateInClusterRequiresSameRunner(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{})
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "in-cluster-target", cluster.ConnectionMethodInCluster, false)
	runner := newCluster(t, client, org.ID, "other-runner", cluster.ConnectionMethodToken, true)

	// in_cluster target + different runner → rejected.
	_, err := svc.Create(ctx, org.ID, httpRepoParams(target.ID, runner.ID))
	if !IsValidation(err) {
		t.Fatalf("mismatched runner error = %v, want ValidationError", err)
	}

	// in_cluster target where the target is itself the (tekton-enabled) runner → ok.
	selfRunner := newCluster(t, client, org.ID, "self", cluster.ConnectionMethodInCluster, true)
	app, err := svc.Create(ctx, org.ID, httpRepoParams(selfRunner.ID, selfRunner.ID))
	if err != nil {
		t.Fatalf("in_cluster self-runner Create: %v", err)
	}
	if app.TargetClusterID != app.RunnerClusterID {
		t.Errorf("expected target == runner")
	}
}

func TestCreateMissingChartFields(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{})
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)

	p := httpRepoParams(target.ID, runner.ID)
	p.Config = map[string]string{helm.ConfigRepoURL: "https://c"} // missing chart
	if _, err := svc.Create(ctx, org.ID, p); !IsValidation(err) {
		t.Fatalf("error = %v, want ValidationError for missing chart", err)
	}
}

func TestCreateTargetClusterFromAnotherOrgRejected(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{})
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	other := newOrg(t, client, "Other")
	foreignTarget := newCluster(t, client, other.ID, "foreign", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)

	_, err := svc.Create(ctx, org.ID, httpRepoParams(foreignTarget.ID, runner.ID))
	if !IsValidation(err) {
		t.Fatalf("error = %v, want ValidationError for cross-org target", err)
	}
}
