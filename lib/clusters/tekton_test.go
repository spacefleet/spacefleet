//go:build integration

package clusters

import (
	"context"
	"testing"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/tektoninstallation"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/tekton"
	"github.com/spacefleet/spacefleet/lib/testsupport"
)

// fakePresence overrides the service's live detection so the upgrade/uninstall
// preconditions can be exercised without a real cluster.
func fakePresence(p *tekton.Presence) func(context.Context, k8s.Connection) (*tekton.Presence, error) {
	return func(context.Context, k8s.Connection) (*tekton.Presence, error) { return p, nil }
}

// TestBeginTektonUpgrade gates the upgrade on a Spacefleet-managed, installed
// Tekton: an unmanaged or absent install is refused, a managed one moves to
// upgrading.
func TestBeginTektonUpgrade(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	org := newOrg(t, client, "Acme")
	c, err := svc.Create(ctx, org.ID, CreateParams{Name: "host", Method: k8s.MethodInCluster})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	svc.detectTekton = fakePresence(&tekton.Presence{Installed: true, ControllerReady: true, Managed: false})
	if _, err := svc.BeginTektonUpgrade(ctx, org.ID, c.ID); !ErrTektonNotManaged(err) {
		t.Fatalf("unmanaged upgrade: got %v, want not-managed", err)
	}

	svc.detectTekton = fakePresence(&tekton.Presence{Installed: false})
	if _, err := svc.BeginTektonUpgrade(ctx, org.ID, c.ID); !ErrTektonNotInstalled(err) {
		t.Fatalf("absent upgrade: got %v, want not-installed", err)
	}

	svc.detectTekton = fakePresence(&tekton.Presence{Installed: true, ControllerReady: true, Managed: true})
	row, err := svc.BeginTektonUpgrade(ctx, org.ID, c.ID)
	if err != nil {
		t.Fatalf("managed upgrade: %v", err)
	}
	if row.Status != tektoninstallation.StatusUpgrading {
		t.Errorf("status = %q, want upgrading", row.Status)
	}
}

// TestBeginTektonUninstall gates removal on a Spacefleet-managed install and
// clears the job-runner flag when it proceeds.
func TestBeginTektonUninstall(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	org := newOrg(t, client, "Acme")
	c, err := svc.Create(ctx, org.ID, CreateParams{Name: "host", Method: k8s.MethodInCluster})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.EnableTekton(ctx, org.ID, c.ID); err != nil {
		t.Fatalf("EnableTekton: %v", err)
	}

	svc.detectTekton = fakePresence(&tekton.Presence{Installed: true, ControllerReady: true, Managed: false})
	if _, err := svc.BeginTektonUninstall(ctx, org.ID, c.ID); !ErrTektonNotManaged(err) {
		t.Fatalf("unmanaged uninstall: got %v, want not-managed", err)
	}

	svc.detectTekton = fakePresence(&tekton.Presence{Installed: true, ControllerReady: true, Managed: true})
	row, err := svc.BeginTektonUninstall(ctx, org.ID, c.ID)
	if err != nil {
		t.Fatalf("managed uninstall: %v", err)
	}
	if row.Status != tektoninstallation.StatusUninstalling {
		t.Errorf("status = %q, want uninstalling", row.Status)
	}
	if row.Enabled {
		t.Error("expected enabled cleared on uninstall")
	}
}

// TestEnableTektonCreatesRow confirms enabling job-running on a fresh cluster
// creates an installation row that is enabled and queued for install.
func TestEnableTektonCreatesRow(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	c, err := svc.Create(ctx, org.ID, CreateParams{Name: "host", Method: k8s.MethodInCluster})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}

	row, err := svc.EnableTekton(ctx, org.ID, c.ID)
	if err != nil {
		t.Fatalf("EnableTekton: %v", err)
	}
	if !row.Enabled {
		t.Error("expected enabled")
	}
	if row.Status != tektoninstallation.StatusInstalling {
		t.Errorf("status = %q, want installing", row.Status)
	}

	// Disable clears the flag without uninstalling (status unchanged).
	row, err = svc.DisableTekton(ctx, org.ID, c.ID)
	if err != nil {
		t.Fatalf("DisableTekton: %v", err)
	}
	if row.Enabled {
		t.Error("expected disabled")
	}
}

// TestTektonOrgScoped confirms another org cannot enable, mark, or read a
// cluster's Tekton installation — the cluster's org scope is the boundary.
func TestTektonOrgScoped(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	orgA := newOrg(t, client, "A")
	orgB := newOrg(t, client, "B")

	c, err := svc.Create(ctx, orgA.ID, CreateParams{Name: "host", Method: k8s.MethodInCluster})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := svc.EnableTekton(ctx, orgB.ID, c.ID); !ent.IsNotFound(err) {
		t.Fatalf("org B enable: got %v, want NotFound", err)
	}
	if _, err := svc.TektonRow(ctx, orgB.ID, c.ID); !ent.IsNotFound(err) {
		t.Fatalf("org B row: got %v, want NotFound", err)
	}
	if err := svc.MarkTekton(ctx, orgB.ID, c.ID, "1", "installed", "v1", ""); !ent.IsNotFound(err) {
		t.Fatalf("org B mark: got %v, want NotFound", err)
	}
}

// TestMarkTektonPersists confirms the worker-facing setter records transitions,
// and that an unknown status is rejected.
func TestMarkTektonPersists(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	c, err := svc.Create(ctx, org.ID, CreateParams{Name: "host", Method: k8s.MethodInCluster})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := svc.MarkTekton(ctx, org.ID, c.ID, "7", "installed", "v0.68.0", ""); err != nil {
		t.Fatalf("MarkTekton: %v", err)
	}
	row, err := svc.TektonRow(ctx, org.ID, c.ID)
	if err != nil {
		t.Fatalf("TektonRow: %v", err)
	}
	if row.Status != tektoninstallation.StatusInstalled || row.InstalledVersion != "v0.68.0" || row.JobID != "7" {
		t.Errorf("row = %+v, want installed/v0.68.0/job 7", row)
	}

	if err := svc.MarkTekton(ctx, org.ID, c.ID, "", "bogus", "", ""); err == nil {
		t.Fatal("expected unknown status to be rejected")
	}
}

// TestTektonCascadeDelete confirms deleting a cluster removes its Tekton row via
// the ON DELETE CASCADE foreign key in the migration.
func TestTektonCascadeDelete(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	c, err := svc.Create(ctx, org.ID, CreateParams{Name: "host", Method: k8s.MethodInCluster})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.EnableTekton(ctx, org.ID, c.ID); err != nil {
		t.Fatalf("EnableTekton: %v", err)
	}

	if err := svc.Delete(ctx, org.ID, c.ID); err != nil {
		t.Fatalf("delete cluster: %v", err)
	}
	n, err := client.TektonInstallation.Query().Where(tektoninstallation.ClusterID(c.ID)).Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("expected tekton row removed by cascade, found %d", n)
	}
}
