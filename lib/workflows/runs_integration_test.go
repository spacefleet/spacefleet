//go:build integration

package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

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

// newApp creates an application in an org with a runner cluster (deploy targets
// live on the components).
func newApp(t *testing.T, client *ent.Client, orgID uuid.UUID, name string) *ent.Application {
	t.Helper()
	runner := newCluster(t, client, orgID, name+"-runner")
	app, err := client.Application.Create().
		SetOrganizationID(orgID).
		SetName(name).
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

// addOutputRun records a succeeded component run that captured the given
// `tofu output -json` blob for a component, finishing at finishedAt — the
// durable row LatestOutputKeys reads. Each gets its own workflow run (outputs
// come from a past run of the app).
func addOutputRun(t *testing.T, client *ent.Client, orgID, appID, componentID uuid.UUID, outputs string, finishedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	wr, err := client.WorkflowRun.Create().
		SetOrganizationID(orgID).
		SetApplicationID(appID).
		SetAction("deploy").
		Save(ctx)
	if err != nil {
		t.Fatalf("create workflow run: %v", err)
	}
	if _, err := client.ComponentRun.Create().
		SetOrganizationID(orgID).
		SetWorkflowRunID(wr.ID).
		SetComponentID(componentID).
		SetStatus(componentrun.StatusSucceeded).
		SetOutputs(outputs).
		SetFinishedAt(finishedAt).
		Save(ctx); err != nil {
		t.Fatalf("create component run: %v", err)
	}
}

// helmInput builds a valid helm ComponentInput targeting the given cluster, for
// the ReplaceWorkflow target-validation tests.
func helmInput(target uuid.UUID) ComponentInput {
	return ComponentInput{
		ID:              uuid.New(),
		Name:            "api",
		Type:            TypeHelm,
		TargetClusterID: &target,
		TargetNamespace: "prod",
		Config: map[string]string{
			"chart_source": "oci",
			"repo_url":     "oci://example.com/charts/app",
		},
	}
}

// TestLatestOutputKeys proves the editor-autocomplete source: per component, the
// keys (and sensitivity) of its LATEST succeeded run's outputs — keys only, never
// a value — with stale runs, failed runs, output-less components, and other
// tenants all excluded.
func TestLatestOutputKeys(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	infra := addComponent(t, client, org.ID, app.ID, "infra", nil)
	ui := addComponent(t, client, org.ID, app.ID, "ui", nil)

	now := time.Now()
	// An older run with a different key set, then the latest — the latest wins.
	addOutputRun(t, client, org.ID, app.ID, infra.ID,
		`{"old_key":{"value":"x","type":"string","sensitive":false}}`, now.Add(-time.Hour))
	addOutputRun(t, client, org.ID, app.ID, infra.ID,
		`{"vpc_id":{"value":"vpc-1","type":"string","sensitive":false},"db_password":{"value":"s","type":"string","sensitive":true}}`, now)

	// A failed run that captured outputs must be ignored (only succeeded counts);
	// it's also the only run touching `ui`, so ui ends up with no known keys.
	failed, err := client.WorkflowRun.Create().
		SetOrganizationID(org.ID).SetApplicationID(app.ID).SetAction("deploy").Save(ctx)
	if err != nil {
		t.Fatalf("create failed run: %v", err)
	}
	if _, err := client.ComponentRun.Create().
		SetOrganizationID(org.ID).SetWorkflowRunID(failed.ID).SetComponentID(ui.ID).
		SetStatus(componentrun.StatusFailed).
		SetOutputs(`{"never":{"value":"y","sensitive":false}}`).
		SetFinishedAt(now).Save(ctx); err != nil {
		t.Fatalf("create failed component run: %v", err)
	}

	// Another org's run for a same-named component must not leak in.
	other := newOrg(t, client, "Other")
	otherApp := newApp(t, client, other.ID, "web")
	otherInfra := addComponent(t, client, other.ID, otherApp.ID, "infra", nil)
	addOutputRun(t, client, other.ID, otherApp.ID, otherInfra.ID,
		`{"leak":{"value":"z","type":"string","sensitive":false}}`, now)

	keys, err := svc.LatestOutputKeys(ctx, org.ID, app.ID)
	if err != nil {
		t.Fatalf("LatestOutputKeys: %v", err)
	}
	// Only infra has known keys; ui (no succeeded outputs) is absent entirely.
	if len(keys) != 1 {
		t.Fatalf("components with outputs = %d, want 1 (%v)", len(keys), keys)
	}
	infraKeys := keys[infra.ID]
	if len(infraKeys) != 2 {
		t.Fatalf("infra keys = %v, want 2 (latest run only)", infraKeys)
	}
	// Sorted by name: db_password (sensitive), then vpc_id; stale old_key is gone.
	if infraKeys[0].Key != "db_password" || !infraKeys[0].Sensitive {
		t.Errorf("infraKeys[0] = %+v, want sensitive db_password", infraKeys[0])
	}
	if infraKeys[1].Key != "vpc_id" || infraKeys[1].Sensitive {
		t.Errorf("infraKeys[1] = %+v, want non-sensitive vpc_id", infraKeys[1])
	}
	for _, ks := range keys {
		for _, k := range ks {
			if k.Key == "leak" || k.Key == "old_key" || k.Key == "never" {
				t.Errorf("unexpected key %q leaked (cross-org / stale / failed)", k.Key)
			}
		}
	}

	// A foreign org reading this app surfaces NotFound, not another tenant's data.
	if _, err := svc.LatestOutputKeys(ctx, other.ID, app.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-org LatestOutputKeys error = %v, want NotFound", err)
	}
}

// TestReplaceWorkflowValidatesComponentTargets proves a helm/manifest node's
// target cluster must exist in the organization: an unknown or cross-org cluster
// is rejected with ErrInvalidTarget, an in-org one is accepted.
func TestReplaceWorkflowValidatesComponentTargets(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	runner := newCluster(t, client, org.ID, "runner")
	app, err := client.Application.Create().
		SetOrganizationID(org.ID).SetName("web").SetRunnerClusterID(runner.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	target := newCluster(t, client, org.ID, "target")

	// Valid: helm targeting an in-org token cluster.
	if _, _, err := svc.ReplaceWorkflow(ctx, org.ID, app.ID, []ComponentInput{helmInput(target.ID)}, nil); err != nil {
		t.Fatalf("valid target: %v", err)
	}
	// Target cluster not in the org → ErrInvalidTarget.
	if _, _, err := svc.ReplaceWorkflow(ctx, org.ID, app.ID, []ComponentInput{helmInput(uuid.New())}, nil); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("unknown target: want ErrInvalidTarget, got %v", err)
	}
	// Cross-org cluster → ErrInvalidTarget.
	other := newOrg(t, client, "Other")
	foreign := newCluster(t, client, other.ID, "foreign")
	if _, _, err := svc.ReplaceWorkflow(ctx, org.ID, app.ID, []ComponentInput{helmInput(foreign.ID)}, nil); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("cross-org target: want ErrInvalidTarget, got %v", err)
	}
}

// TestReplaceWorkflowInClusterTargetRequiresRunner proves the relocated rule: an
// in-cluster target is only reachable from a pod in that same cluster, so it must
// be the application's runner.
func TestReplaceWorkflowInClusterTargetRequiresRunner(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	inCluster, err := client.Cluster.Create().
		SetOrganizationID(org.ID).SetName("self").SetConnectionMethod(cluster.ConnectionMethodInCluster).Save(ctx)
	if err != nil {
		t.Fatalf("create in-cluster: %v", err)
	}
	app, err := client.Application.Create().
		SetOrganizationID(org.ID).SetName("web").SetRunnerClusterID(inCluster.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	// In-cluster target that is the runner → ok.
	if _, _, err := svc.ReplaceWorkflow(ctx, org.ID, app.ID, []ComponentInput{helmInput(inCluster.ID)}, nil); err != nil {
		t.Fatalf("in-cluster target == runner: %v", err)
	}
	// A different in-cluster target (not the runner) → ErrInvalidTarget.
	otherInCluster, err := client.Cluster.Create().
		SetOrganizationID(org.ID).SetName("other").SetConnectionMethod(cluster.ConnectionMethodInCluster).Save(ctx)
	if err != nil {
		t.Fatalf("create other in-cluster: %v", err)
	}
	if _, _, err := svc.ReplaceWorkflow(ctx, org.ID, app.ID, []ComponentInput{helmInput(otherInCluster.ID)}, nil); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("in-cluster target != runner: want ErrInvalidTarget, got %v", err)
	}
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

// TestBeginRunExpandsTerraform proves a single authored OpenTofu component
// fans out, at run time, into a plan unit (the authored id) and an apply unit (a
// derived id) — two pending ComponentRuns whose ids match the two expanded
// snapshot nodes, with the apply unit gated by the component's requires_approval.
func TestBeginRunExpandsTerraform(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")

	tf, err := client.Component.Create().
		SetOrganizationID(org.ID).
		SetApplicationID(app.ID).
		SetName("infra").
		SetType("terraform").
		SetConfig(map[string]string{"repo_url": "https://github.com/acme/infra", "path": "envs/prod", "backend": "s3"}).
		SetRequiresApproval(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("create terraform component: %v", err)
	}

	run, err := svc.BeginRun(ctx, org.ID, app.ID, ActionDeploy)
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}

	// The snapshot holds two execution nodes: plan (authored id) and apply (derived).
	var snap GraphSnapshot
	if err := json.Unmarshal([]byte(run.Graph), &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snap.Nodes) != 2 {
		t.Fatalf("snapshot nodes = %d, want 2 (plan + apply)", len(snap.Nodes))
	}
	applyID := deriveApplyID(tf.ID)
	var sawPlan, sawApply bool
	for _, n := range snap.Nodes {
		switch n.ID {
		case tf.ID:
			sawPlan = true
			if n.Config["command"] != "plan" || n.RequiresApproval {
				t.Errorf("plan unit wrong: %+v", n)
			}
		case applyID:
			sawApply = true
			if n.Config["command"] != "apply" || !n.RequiresApproval {
				t.Errorf("apply unit wrong: %+v", n)
			}
			if n.ComponentID != tf.ID {
				t.Errorf("apply ComponentID = %s, want authored %s", n.ComponentID, tf.ID)
			}
			if len(n.DependsOn) != 1 || n.DependsOn[0] != tf.ID {
				t.Errorf("apply deps = %v, want [%s]", n.DependsOn, tf.ID)
			}
		default:
			t.Errorf("unexpected snapshot node id %s", n.ID)
		}
	}
	if !sawPlan || !sawApply {
		t.Fatalf("expected both plan and apply units; sawPlan=%v sawApply=%v", sawPlan, sawApply)
	}

	// Two ComponentRuns, one per execution unit, keyed by the unit ids.
	_, steps, err := svc.GetRun(ctx, org.ID, app.ID, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("component runs = %d, want 2", len(steps))
	}
	ids := map[uuid.UUID]bool{}
	for _, cr := range steps {
		ids[cr.ComponentID] = true
		if cr.Status != componentrun.StatusPending {
			t.Errorf("step %s status = %q, want pending", cr.Name, cr.Status)
		}
	}
	if !ids[tf.ID] || !ids[applyID] {
		t.Errorf("component run ids = %v, want plan %s + apply %s", ids, tf.ID, applyID)
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

// TestListOrgRuns proves the global run-history query returns every application's
// runs in the org, newest-first, capped by limit, and strictly org-scoped — a
// second org's runs never appear. It powers the cross-application runs index.
func TestListOrgRuns(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	orgA := newOrg(t, client, "A")
	orgB := newOrg(t, client, "B")
	web := newApp(t, client, orgA.ID, "web")
	api := newApp(t, client, orgA.ID, "api")
	addComponent(t, client, orgA.ID, web.ID, "c", nil)
	addComponent(t, client, orgA.ID, api.ID, "c", nil)
	other := newApp(t, client, orgB.ID, "other")
	addComponent(t, client, orgB.ID, other.ID, "c", nil)

	// Three runs in org A across two apps, plus one in org B. created_at is set by
	// ent on insert, so begin them in a known order; the query returns newest-first.
	r1, err := svc.BeginRun(ctx, orgA.ID, web.ID, ActionDeploy)
	if err != nil {
		t.Fatalf("BeginRun web: %v", err)
	}
	// The app's in-flight gate refuses a second run while r1 is pending, so settle
	// it before starting another run on the same app.
	if err := svc.MarkRun(ctx, orgA.ID, r1.ID, "succeeded", "done"); err != nil {
		t.Fatalf("MarkRun r1: %v", err)
	}
	// created_at is the only sort key; small gaps keep the newest-first order
	// deterministic rather than relying on sub-millisecond insert timing.
	time.Sleep(2 * time.Millisecond)
	r2, err := svc.BeginRun(ctx, orgA.ID, api.ID, ActionPreview)
	if err != nil {
		t.Fatalf("BeginRun api: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	r3, err := svc.BeginRun(ctx, orgA.ID, web.ID, ActionDeploy)
	if err != nil {
		t.Fatalf("BeginRun web #2: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := svc.BeginRun(ctx, orgB.ID, other.ID, ActionDeploy); err != nil {
		t.Fatalf("BeginRun other: %v", err)
	}

	runs, err := svc.ListOrgRuns(ctx, orgA.ID, 100)
	if err != nil {
		t.Fatalf("ListOrgRuns: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("org A runs = %d, want 3 (org B's run must not leak)", len(runs))
	}
	// Newest-first: r3, then r2, then r1.
	wantOrder := []uuid.UUID{r3.ID, r2.ID, r1.ID}
	for i, want := range wantOrder {
		if runs[i].ID != want {
			t.Errorf("runs[%d] = %v, want %v (newest-first across apps)", i, runs[i].ID, want)
		}
	}

	// The limit caps the result to the newest N.
	capped, err := svc.ListOrgRuns(ctx, orgA.ID, 2)
	if err != nil {
		t.Fatalf("ListOrgRuns capped: %v", err)
	}
	if len(capped) != 2 || capped[0].ID != r3.ID || capped[1].ID != r2.ID {
		t.Errorf("capped runs = %v, want newest two [%v %v]", capped, r3.ID, r2.ID)
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

// TestReaperLeavesParkedRunsAlone proves the stuck-run reaper never touches a run
// parked at awaiting_approval — even one whose River job is positively gone and
// whose started_at is far past the reap cutoff. The reaper only fails runs stuck in
// "running" (its query filters StatusEQ(running)); a gate can stay open for days, so
// a parked run must not be mistaken for an abandoned one. Without that status filter
// this run would be reaped, since the liveJob below reports the job as gone.
func TestReaperLeavesParkedRunsAlone(t *testing.T) {
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
	if err := svc.SetRunJob(ctx, org.ID, run.ID, "job-parked"); err != nil {
		t.Fatalf("SetRunJob: %v", err)
	}
	// Drive the run to running, then park it awaiting approval, then backdate its
	// started_at well past the reap cutoff so only the status filter protects it.
	if err := svc.MarkRun(ctx, org.ID, run.ID, string(workflowrun.StatusRunning), "running"); err != nil {
		t.Fatalf("MarkRun running: %v", err)
	}
	if err := svc.MarkRun(ctx, org.ID, run.ID, string(workflowrun.StatusAwaitingApproval), "awaiting manual approval"); err != nil {
		t.Fatalf("MarkRun awaiting_approval: %v", err)
	}
	if err := client.WorkflowRun.UpdateOneID(run.ID).
		SetStartedAt(time.Now().Add(-2 * reapMaxLifetime)).
		Exec(ctx); err != nil {
		t.Fatalf("backdate started_at: %v", err)
	}

	// liveJob reports the job as gone — the only thing keeping this run alive is the
	// reaper's running-only status filter.
	jobGone := func(context.Context, string) (bool, error) { return false, nil }
	reaped, err := svc.ReapStuckRuns(ctx, reapMaxLifetime, jobGone)
	if err != nil {
		t.Fatalf("ReapStuckRuns: %v", err)
	}
	if reaped != 0 {
		t.Fatalf("reaped = %d, want 0 (a parked run must not be reaped)", reaped)
	}

	after, _, err := svc.GetRun(ctx, org.ID, app.ID, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if after.Status != workflowrun.StatusAwaitingApproval {
		t.Fatalf("run status after reap = %q, want awaiting_approval (untouched)", after.Status)
	}
}
