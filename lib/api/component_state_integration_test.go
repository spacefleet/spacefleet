//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/ent/componentrun"
	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// TestGetComponentState exercises the component-state endpoint end to end: the
// latest successful apply's outputs and resource inventory come back for an
// editor (sensitive values included), a viewer gets the inventory and the
// non-sensitive outputs with the sensitive value withheld, an older apply is
// superseded by the latest, a component with no successful apply is a 404, and
// another org's component is unreachable.
func TestGetComponentState(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()

	editorTok, orgID := h.member("editor", membership.RoleEditor)
	viewer, err := h.client.User.Create().SetOidcSubject("viewer").SetEmail("viewer@test.local").Save(ctx)
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	if _, err := h.client.Membership.Create().
		SetOrganizationID(orgID).SetUserID(viewer.ID).SetRole(membership.RoleViewer).Save(ctx); err != nil {
		t.Fatalf("add viewer membership: %v", err)
	}

	runner, err := h.client.Cluster.Create().
		SetOrganizationID(orgID).SetName("runner").SetConnectionMethod(cluster.ConnectionMethodToken).Save(ctx)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	app, err := h.client.Application.Create().
		SetOrganizationID(orgID).SetName("web").SetRunnerClusterID(runner.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	infra, err := h.client.Component.Create().
		SetOrganizationID(orgID).SetApplicationID(app.ID).SetName("infra").SetType("terraform").Save(ctx)
	if err != nil {
		t.Fatalf("create component: %v", err)
	}
	never, err := h.client.Component.Create().
		SetOrganizationID(orgID).SetApplicationID(app.ID).SetName("never").SetType("terraform").Save(ctx)
	if err != nil {
		t.Fatalf("create component: %v", err)
	}

	// Two successful applies: an older one with a different inventory, then the
	// latest — only the latest is reported.
	seedApply := func(outputs, resources string, finished time.Time) uuid.UUID {
		wr, err := h.client.WorkflowRun.Create().
			SetOrganizationID(orgID).SetApplicationID(app.ID).SetAction("deploy").Save(ctx)
		if err != nil {
			t.Fatalf("create workflow run: %v", err)
		}
		if _, err := h.client.ComponentRun.Create().
			SetOrganizationID(orgID).SetWorkflowRunID(wr.ID).SetComponentID(workflows.DeriveApplyID(infra.ID)).
			SetStatus(componentrun.StatusSucceeded).
			SetOutputs(outputs).SetResources(resources).
			SetFinishedAt(finished).Save(ctx); err != nil {
			t.Fatalf("create component run: %v", err)
		}
		return wr.ID
	}
	now := time.Now()
	seedApply(`{"old":{"value":"x","type":"string","sensitive":false}}`,
		`[{"address":"aws_s3_bucket.old","mode":"managed","type":"aws_s3_bucket","name":"old","provider":"p","id":"old"}]`,
		now.Add(-time.Hour))
	latestRun := seedApply(
		`{"vpc_id":{"value":"vpc-1","type":"string","sensitive":false},"db_password":{"value":"hunter2","type":"string","sensitive":true}}`,
		`[{"address":"module.net.aws_vpc.main","mode":"managed","type":"aws_vpc","name":"main","provider":"registry.opentofu.org/hashicorp/aws","id":"vpc-1"},{"address":"data.aws_ami.x","mode":"data","type":"aws_ami","name":"x","provider":"p","id":null}]`,
		now)

	path := "/api/applications/" + app.ID.String() + "/components/" + infra.ID.String() + "/state"

	// Editor: the latest apply, outputs with the sensitive value, both resources.
	rec := testReq{method: http.MethodGet, path: path, token: editorTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("editor got %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	var state ComponentState
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.RunId == nil || *state.RunId != latestRun {
		t.Errorf("run_id = %v, want the latest apply's run %s", state.RunId, latestRun)
	}
	if len(state.Resources) != 2 || state.Resources[0].Address != "module.net.aws_vpc.main" || state.Resources[0].Id == nil {
		t.Errorf("resources = %+v", state.Resources)
	}
	if state.Resources[1].Id != nil {
		t.Errorf("a null id should be absent, got %v", state.Resources[1].Id)
	}
	if state.Outputs == nil || (*state.Outputs)["db_password"].Value != "hunter2" {
		t.Errorf("editor should see the sensitive output value: %+v", state.Outputs)
	}

	// Viewer: same inventory, sensitive output value withheld.
	rec = testReq{method: http.MethodGet, path: path, token: "viewer", orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer got %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(state.Resources) != 2 {
		t.Errorf("viewer resources = %+v", state.Resources)
	}
	if o := (*state.Outputs)["db_password"]; !o.Sensitive || o.Value != nil {
		t.Errorf("viewer must not see the sensitive value: %+v", o)
	}
	if contains(rec.Body.String(), "hunter2") {
		t.Errorf("viewer body leaked the sensitive value: %s", rec.Body.String())
	}

	// A component with no successful apply: 404.
	rec = testReq{method: http.MethodGet, path: "/api/applications/" + app.ID.String() + "/components/" + never.ID.String() + "/state", token: editorTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("never-applied got %d, want 404\n%s", rec.Code, rec.Body.String())
	}

	// Another org's editor cannot reach it.
	otherTok, otherOrg := h.member("other", membership.RoleEditor)
	rec = testReq{method: http.MethodGet, path: path, token: otherTok, orgID: otherOrg.String()}.do(t, h.handler)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-org got %d, want 404\n%s", rec.Code, rec.Body.String())
	}
}

// TestGetComponentStateLock proves the state view surfaces a stuck state
// lock: when the component's latest settled step failed on "Error acquiring
// the state lock", editors get the parsed lock (id, who, created) and
// viewers do not; a later step that got through clears it.
func TestGetComponentStateLock(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	editorTok, orgID := h.member("editor", membership.RoleEditor)
	viewer, err := h.client.User.Create().SetOidcSubject("viewer").SetEmail("viewer@test.local").Save(ctx)
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	if _, err := h.client.Membership.Create().
		SetOrganizationID(orgID).SetUserID(viewer.ID).SetRole(membership.RoleViewer).Save(ctx); err != nil {
		t.Fatalf("add viewer membership: %v", err)
	}
	runner, err := h.client.Cluster.Create().
		SetOrganizationID(orgID).SetName("runner").SetConnectionMethod(cluster.ConnectionMethodToken).Save(ctx)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	app, err := h.client.Application.Create().
		SetOrganizationID(orgID).SetName("web").SetRunnerClusterID(runner.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	infra, err := h.client.Component.Create().
		SetOrganizationID(orgID).SetApplicationID(app.ID).SetName("infra").SetType("terraform").Save(ctx)
	if err != nil {
		t.Fatalf("create component: %v", err)
	}
	seedStep := func(action workflowrun.Action, componentID uuid.UUID, status componentrun.Status, logs string, finished time.Time) uuid.UUID {
		wr, err := h.client.WorkflowRun.Create().SetOrganizationID(orgID).SetApplicationID(app.ID).SetAction(action).Save(ctx)
		if err != nil {
			t.Fatalf("create run: %v", err)
		}
		cr := h.client.ComponentRun.Create().
			SetOrganizationID(orgID).SetWorkflowRunID(wr.ID).SetComponentID(componentID).SetType("terraform").
			SetStatus(status).SetLogs(logs).SetFinishedAt(finished)
		if status == componentrun.StatusSucceeded && componentID != infra.ID {
			cr.SetResources(`[{"address":"aws_instance.web","mode":"managed","type":"aws_instance","name":"web"}]`)
		}
		if _, err := cr.Save(ctx); err != nil {
			t.Fatalf("create step: %v", err)
		}
		return wr.ID
	}
	now := time.Now()
	// The apply that gives the component a recorded state, then a plan that
	// died on the lock.
	seedStep("deploy", workflows.DeriveApplyID(infra.ID), componentrun.StatusSucceeded, "", now.Add(-2*time.Hour))
	lockLogs := "╷\n│ Error: Error acquiring the state lock\n│ \n│ Error message: ConditionalCheckFailedException: The conditional request failed\n│ Lock Info:\n│   ID:        6ea66d5f-8c3c-4d0d-8ecf-1d0e7a3f1c0f\n│   Path:      acme-state/prod/terraform.tfstate\n│   Operation: OperationTypeApply\n│   Who:       root@pod\n│   Version:   1.8.5\n│   Created:   2026-09-01 12:34:56 +0000 UTC\n│   Info:      \n│ \n╵\n"
	lockedRun := seedStep("deploy", infra.ID, componentrun.StatusFailed, lockLogs, now.Add(-time.Hour))

	path := "/api/applications/" + app.ID.String() + "/components/" + infra.ID.String() + "/state"
	get := func(token string) ComponentState {
		t.Helper()
		rec := testReq{method: http.MethodGet, path: path, token: token, orgID: orgID.String()}.do(t, h.handler)
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d\n%s", rec.Code, rec.Body.String())
		}
		var state ComponentState
		if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return state
	}
	state := get(editorTok)
	if state.Lock == nil || state.Lock.Id != "6ea66d5f-8c3c-4d0d-8ecf-1d0e7a3f1c0f" || state.Lock.RunId != lockedRun {
		t.Fatalf("editor lock = %+v, want the parsed lock on run %s", state.Lock, lockedRun)
	}
	if *state.Lock.Who != "root@pod" || *state.Lock.Path != "acme-state/prod/terraform.tfstate" || *state.Lock.Created != "2026-09-01 12:34:56 +0000 UTC" {
		t.Errorf("lock fields = %+v", state.Lock)
	}
	if len(state.Resources) != 1 {
		t.Errorf("the recorded state must still be reported alongside the lock: %+v", state.Resources)
	}
	if state = get("viewer"); state.Lock != nil {
		t.Errorf("viewer must not see the lock: %+v", state.Lock)
	}

	// A later step that got through (a force-unlock, then a plan) clears it.
	seedStep("state_op", infra.ID, componentrun.StatusSucceeded, "Unlocked", now.Add(-30*time.Minute))
	if state = get(editorTok); state.Lock != nil {
		t.Errorf("after a succeeded step the lock must be gone: %+v", state.Lock)
	}
	// A later failure for another reason is not a lock either.
	seedStep("deploy", infra.ID, componentrun.StatusFailed, "Error: Backend initialization required\n", now)
	if state = get(editorTok); state.Lock != nil {
		t.Errorf("a non-lock failure must not report a lock: %+v", state.Lock)
	}

	// A component that has never applied but whose first plan died on the
	// lock: an editor gets a state view carrying only the lock (no recorded
	// run, no resources); a viewer still gets the 404.
	never, err := h.client.Component.Create().
		SetOrganizationID(orgID).SetApplicationID(app.ID).SetName("never").SetType("terraform").Save(ctx)
	if err != nil {
		t.Fatalf("create component: %v", err)
	}
	neverPath := "/api/applications/" + app.ID.String() + "/components/" + never.ID.String() + "/state"
	rec := testReq{method: http.MethodGet, path: neverPath, token: editorTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("never-applied, no lock: got %d, want 404", rec.Code)
	}
	wr, _ := h.client.WorkflowRun.Create().SetOrganizationID(orgID).SetApplicationID(app.ID).SetAction("deploy").Save(ctx)
	if _, err := h.client.ComponentRun.Create().
		SetOrganizationID(orgID).SetWorkflowRunID(wr.ID).SetComponentID(never.ID).SetType("terraform").
		SetStatus(componentrun.StatusFailed).SetLogs(lockLogs).SetFinishedAt(now).Save(ctx); err != nil {
		t.Fatalf("create step: %v", err)
	}
	rec = testReq{method: http.MethodGet, path: neverPath, token: editorTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("never-applied, locked: got %d\n%s", rec.Code, rec.Body.String())
	}
	var lockOnly ComponentState
	if err := json.Unmarshal(rec.Body.Bytes(), &lockOnly); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if lockOnly.RunId != nil || lockOnly.ComponentRunId != nil || len(lockOnly.Resources) != 0 || lockOnly.Lock == nil || lockOnly.Lock.RunId != wr.ID {
		t.Errorf("lock-only state = %s", rec.Body.String())
	}
	rec = testReq{method: http.MethodGet, path: neverPath, token: "viewer", orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusNotFound {
		t.Errorf("viewer, never-applied, locked: got %d, want 404", rec.Code)
	}
}

// TestGetComponentStateDrift proves the state view carries the latest drift
// check's verdict: a succeeded refresh-only plan that found drift reports
// has_drift with the drifted addresses (no diffs), and a later failed check
// reports its failed status with has_drift=false (unknown, not clean).
func TestGetComponentStateDrift(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	editorTok, orgID := h.member("editor", membership.RoleEditor)

	runner, err := h.client.Cluster.Create().
		SetOrganizationID(orgID).SetName("runner").SetConnectionMethod(cluster.ConnectionMethodToken).Save(ctx)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	app, err := h.client.Application.Create().
		SetOrganizationID(orgID).SetName("web").SetRunnerClusterID(runner.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	infra, err := h.client.Component.Create().
		SetOrganizationID(orgID).SetApplicationID(app.ID).SetName("infra").SetType("terraform").Save(ctx)
	if err != nil {
		t.Fatalf("create component: %v", err)
	}
	// The apply that gives the component a recorded state.
	applyRun, _ := h.client.WorkflowRun.Create().SetOrganizationID(orgID).SetApplicationID(app.ID).SetAction("deploy").Save(ctx)
	if _, err := h.client.ComponentRun.Create().
		SetOrganizationID(orgID).SetWorkflowRunID(applyRun.ID).SetComponentID(workflows.DeriveApplyID(infra.ID)).
		SetStatus(componentrun.StatusSucceeded).SetResources(`[{"address":"aws_instance.web","mode":"managed","type":"aws_instance","name":"web"}]`).
		SetFinishedAt(time.Now().Add(-2 * time.Hour)).Save(ctx); err != nil {
		t.Fatalf("create apply step: %v", err)
	}
	seedDrift := func(status componentrun.Status, logs string, finished time.Time) {
		wr, err := h.client.WorkflowRun.Create().SetOrganizationID(orgID).SetApplicationID(app.ID).SetAction("drift").Save(ctx)
		if err != nil {
			t.Fatalf("create drift run: %v", err)
		}
		if _, err := h.client.ComponentRun.Create().
			SetOrganizationID(orgID).SetWorkflowRunID(wr.ID).SetComponentID(infra.ID).SetType("terraform").
			SetStatus(status).SetLogs(logs).SetFinishedAt(finished).Save(ctx); err != nil {
			t.Fatalf("create drift step: %v", err)
		}
	}
	driftLogs := "OpenTofu detected the following changes made outside of OpenTofu since the\nlast \"tofu apply\" which may have affected this plan:\n\n  # aws_instance.web has been changed\n  ~ resource \"aws_instance\" \"web\" {\n      ~ instance_type = \"t3.micro\" -> \"t3.small\"\n    }\n\nThis is a refresh-only plan, so OpenTofu will not take any actions to undo\nthese.\n"
	seedDrift(componentrun.StatusSucceeded, driftLogs, time.Now().Add(-time.Hour))

	path := "/api/applications/" + app.ID.String() + "/components/" + infra.ID.String() + "/state"
	rec := testReq{method: http.MethodGet, path: path, token: editorTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d\n%s", rec.Code, rec.Body.String())
	}
	var state ComponentState
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.Drift == nil || !state.Drift.HasDrift || state.Drift.Status != ComponentRunStatusSucceeded {
		t.Fatalf("drift = %+v, want a succeeded check with drift", state.Drift)
	}
	if d := *state.Drift.Drift; len(d) != 1 || d[0].Address != "aws_instance.web" || d[0].Action != PlanResourceChangeActionDriftUpdate || d[0].Diff != nil {
		t.Errorf("drifted resources = %+v", d)
	}

	// A newer check that failed: status failed, verdict unknown (has_drift=false).
	seedDrift(componentrun.StatusFailed, "Error: Backend initialization required\n", time.Now())
	rec = testReq{method: http.MethodGet, path: path, token: editorTok, orgID: orgID.String()}.do(t, h.handler)
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.Drift == nil || state.Drift.HasDrift || state.Drift.Status != ComponentRunStatusFailed {
		t.Errorf("after a failed check drift = %+v, want failed/no verdict", state.Drift)
	}
}
