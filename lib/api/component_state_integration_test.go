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
	if state.RunId != latestRun {
		t.Errorf("run_id = %s, want the latest apply's run %s", state.RunId, latestRun)
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
