//go:build integration

package workflows

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/ent/componentrun"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
	"github.com/spacefleet/spacefleet/lib/testsupport"
)

// This file is the DB-backed half of the run tests: BeginRun's transactional
// snapshot + ComponentRun fan-out and its in-flight gate, plus the cross-org
// isolation of every run read. It is integration-tagged because it needs a real
// Postgres (the tag-free runs_test.go covers the pure helpers — validAction and
// snapshotComponents). Each test gets an isolated, freshly-migrated database via
// the testsupport harness.

// newOrg creates a bare organization and returns it.
func newOrg(t *testing.T, client *ent.Client, name string) *ent.Organization {
	t.Helper()
	org, err := client.Organization.Create().SetName(name).Save(context.Background())
	if err != nil {
		t.Fatalf("create org %q: %v", name, err)
	}
	return org
}

// newCluster creates a cluster in an org (the application FKs require one).
func newCluster(t *testing.T, client *ent.Client, orgID uuid.UUID, name string) *ent.Cluster {
	t.Helper()
	c, err := client.Cluster.Create().
		SetOrganizationID(orgID).
		SetName(name).
		SetConnectionMethod(cluster.ConnectionMethodToken).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create cluster %q: %v", name, err)
	}
	return c
}

// newApp creates an application in an org with a target+runner cluster.
func newApp(t *testing.T, client *ent.Client, orgID uuid.UUID, name string) *ent.Application {
	t.Helper()
	target := newCluster(t, client, orgID, name+"-target")
	runner := newCluster(t, client, orgID, name+"-runner")
	app, err := client.Application.Create().
		SetOrganizationID(orgID).
		SetName(name).
		SetTargetNamespace("apps").
		SetTargetClusterID(target.ID).
		SetRunnerClusterID(runner.ID).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create app %q: %v", name, err)
	}
	return app
}

// addComponent creates one workflow node on an app, returning it.
func addComponent(t *testing.T, client *ent.Client, orgID, appID uuid.UUID, name string, config map[string]string) *ent.Component {
	t.Helper()
	c, err := client.Component.Create().
		SetOrganizationID(orgID).
		SetApplicationID(appID).
		SetName(name).
		SetType("helm").
		SetConfig(config).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create component %q: %v", name, err)
	}
	return c
}

// TestBeginRunSnapshotAndComponentRuns proves a successful BeginRun snapshots the
// live component graph onto the run and creates one pending ComponentRun per node,
// all org-scoped and in a single transaction.
func TestBeginRunSnapshotAndComponentRuns(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	c1 := addComponent(t, client, org.ID, app.ID, "api", map[string]string{"values": "secret: 1"})
	c2 := addComponent(t, client, org.ID, app.ID, "ui", map[string]string{"chart": "ui"})

	run, err := svc.BeginRun(ctx, org.ID, app.ID, ActionDeploy)
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if run.Status != workflowrun.StatusPending {
		t.Errorf("run status = %q, want pending", run.Status)
	}
	if string(run.Action) != ActionDeploy {
		t.Errorf("run action = %q, want %q", run.Action, ActionDeploy)
	}

	// The graph snapshot is persisted and round-trips to both nodes' as-run config.
	var snap GraphSnapshot
	if err := json.Unmarshal([]byte(run.Graph), &snap); err != nil {
		t.Fatalf("decode snapshot %q: %v", run.Graph, err)
	}
	if len(snap.Nodes) != 2 {
		t.Fatalf("snapshot nodes = %d, want 2", len(snap.Nodes))
	}
	if snap.Nodes[0].Config["values"] != "secret: 1" {
		t.Errorf("snapshot did not copy as-run config: %+v", snap.Nodes[0])
	}

	// One ComponentRun per node, pending, with the component denormalized.
	_, steps, err := svc.GetRun(ctx, org.ID, app.ID, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("component runs = %d, want 2", len(steps))
	}
	for _, cr := range steps {
		if cr.Status != componentrun.StatusPending {
			t.Errorf("component run %s status = %q, want pending", cr.Name, cr.Status)
		}
		if cr.ComponentID != c1.ID && cr.ComponentID != c2.ID {
			t.Errorf("component run %s has unexpected component id %s", cr.Name, cr.ComponentID)
		}
	}
}

// TestBeginRunInFlightConflict proves a second BeginRun while a run is still
// pending/running is refused with ErrRunInFlight (the handler's 409), and that no
// orphan run is created.
func TestBeginRunInFlightConflict(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	addComponent(t, client, org.ID, app.ID, "api", nil)

	first, err := svc.BeginRun(ctx, org.ID, app.ID, ActionDeploy)
	if err != nil {
		t.Fatalf("first BeginRun: %v", err)
	}

	// A second run while the first is pending is refused.
	if _, err := svc.BeginRun(ctx, org.ID, app.ID, ActionDeploy); err != ErrRunInFlight {
		t.Fatalf("second BeginRun error = %v, want ErrRunInFlight", err)
	}
	// Still in running state is in-flight too.
	if err := svc.MarkRun(ctx, org.ID, first.ID, string(workflowrun.StatusRunning), ""); err != nil {
		t.Fatalf("MarkRun running: %v", err)
	}
	if _, err := svc.BeginRun(ctx, org.ID, app.ID, ActionDeploy); err != ErrRunInFlight {
		t.Fatalf("BeginRun while running error = %v, want ErrRunInFlight", err)
	}

	// Once the first settles, a new run is allowed again.
	if err := svc.MarkRun(ctx, org.ID, first.ID, string(workflowrun.StatusSucceeded), ""); err != nil {
		t.Fatalf("MarkRun succeeded: %v", err)
	}
	if _, err := svc.BeginRun(ctx, org.ID, app.ID, ActionDeploy); err != nil {
		t.Fatalf("BeginRun after settle: %v", err)
	}

	// Exactly two runs exist (no orphan from a refused attempt).
	runs, err := svc.ListRuns(ctx, org.ID, app.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2 (the refused attempts created none)", len(runs))
	}
}

// TestBeginRunInvalidActionAndForeignApp covers BeginRun's two early rejections:
// an unknown action (ErrInvalidAction) and an application in another org (the
// scoped getApp returns NotFound).
func TestBeginRunInvalidActionAndForeignApp(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	orgA := newOrg(t, client, "A")
	orgB := newOrg(t, client, "B")
	app := newApp(t, client, orgA.ID, "web")

	if _, err := svc.BeginRun(ctx, orgA.ID, app.ID, "upgrade"); err != ErrInvalidAction {
		t.Errorf("invalid action error = %v, want ErrInvalidAction", err)
	}
	// Org B can't begin a run on org A's app.
	if _, err := svc.BeginRun(ctx, orgB.ID, app.ID, ActionDeploy); !ent.IsNotFound(err) {
		t.Errorf("cross-org BeginRun error = %v, want NotFound", err)
	}
}

// TestRunReadsAreCrossOrgIsolated proves a run (and its component runs) created in
// org A is invisible to org B: GetRun, GetComponentRun, and ListRuns all surface
// NotFound for the foreign org rather than leaking another tenant's run.
func TestRunReadsAreCrossOrgIsolated(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	orgA := newOrg(t, client, "A")
	orgB := newOrg(t, client, "B")
	app := newApp(t, client, orgA.ID, "web")
	addComponent(t, client, orgA.ID, app.ID, "api", nil)

	run, err := svc.BeginRun(ctx, orgA.ID, app.ID, ActionDeploy)
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	_, steps, err := svc.GetRun(ctx, orgA.ID, app.ID, run.ID)
	if err != nil {
		t.Fatalf("GetRun (owner): %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("component runs = %d, want 1", len(steps))
	}
	crID := steps[0].ID

	// Org B can read none of it.
	if _, _, err := svc.GetRun(ctx, orgB.ID, app.ID, run.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-org GetRun error = %v, want NotFound", err)
	}
	if _, err := svc.GetComponentRun(ctx, orgB.ID, app.ID, run.ID, crID); !ent.IsNotFound(err) {
		t.Errorf("cross-org GetComponentRun error = %v, want NotFound", err)
	}
	if _, err := svc.ListRuns(ctx, orgB.ID, app.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-org ListRuns error = %v, want NotFound", err)
	}

	// And the owner still reads it cleanly (the scoping isn't over-broad).
	if _, err := svc.GetComponentRun(ctx, orgA.ID, app.ID, run.ID, crID); err != nil {
		t.Errorf("owner GetComponentRun: %v", err)
	}
}

// TestCancelRunIsDurableAgainstExecutorWrites locks in the regression fix: a
// cancelled run must stay failed even if the worker (which may be mid-flight when
// the cancel lands) subsequently calls MarkRun with a terminal status. The guard
// is MarkRun's in-flight predicate — once a run has settled it is frozen, so the
// executor's final MarkRun(succeeded/partial/failed) becomes a no-op and cannot
// resurrect the cancelled run. It also covers the idempotency of a second cancel.
func TestCancelRunIsDurableAgainstExecutorWrites(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	addComponent(t, client, org.ID, app.ID, "api", nil)

	run, err := svc.BeginRun(ctx, org.ID, app.ID, ActionDeploy)
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	// Simulate the worker having marked the run running before the cancel arrives.
	if err := svc.MarkRun(ctx, org.ID, run.ID, string(workflowrun.StatusRunning), "running"); err != nil {
		t.Fatalf("MarkRun running: %v", err)
	}

	cancelled, err := svc.CancelRun(ctx, org.ID, app.ID, run.ID)
	if err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if cancelled.Status != workflowrun.StatusFailed {
		t.Fatalf("cancelled status = %q, want failed", cancelled.Status)
	}
	// The component run was settled by the cancel, not left pending.
	_, steps, err := svc.GetRun(ctx, org.ID, app.ID, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if steps[0].Status != componentrun.StatusFailed {
		t.Fatalf("component run status = %q, want failed", steps[0].Status)
	}

	// The worker, still finishing its attempt, tries to settle the run succeeded.
	// The guard must make this a no-op so the cancel is durable.
	if err := svc.MarkRun(ctx, org.ID, run.ID, string(workflowrun.StatusSucceeded), "workflow succeeded"); !ent.IsNotFound(err) {
		t.Fatalf("MarkRun(succeeded) on cancelled run error = %v, want NotFound (no-op)", err)
	}
	after, _, err := svc.GetRun(ctx, org.ID, app.ID, run.ID)
	if err != nil {
		t.Fatalf("GetRun after executor write: %v", err)
	}
	if after.Status != workflowrun.StatusFailed {
		t.Fatalf("run status after executor write = %q, want failed (cancel must be durable)", after.Status)
	}

	// A second cancel is a conflict (the run already settled), not a double-settle.
	if _, err := svc.CancelRun(ctx, org.ID, app.ID, run.ID); err != ErrRunNotInFlight {
		t.Fatalf("second CancelRun error = %v, want ErrRunNotInFlight", err)
	}

	// The in-flight gate cleared: a fresh run can start.
	if _, err := svc.BeginRun(ctx, org.ID, app.ID, ActionDeploy); err != nil {
		t.Fatalf("BeginRun after cancel: %v", err)
	}
}
