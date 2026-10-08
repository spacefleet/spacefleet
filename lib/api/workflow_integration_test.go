//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// TestWorkflowStagesRoundTrip drives the workflow endpoints end to end: an
// editor replaces the workflow with ordered stages and gets them back in
// order, a viewer reads them with secret config redacted, validation failures
// (a same-stage output reference, a blank stage name) are 400s that leave the
// saved workflow untouched, and a run of the workflow lists — and details —
// its stages, with the OpenTofu component's plan and apply steps folded into
// one component.
func TestWorkflowStagesRoundTrip(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()

	editorTok, orgID := h.member("editor", membership.RoleEditor)
	const viewerTok = "viewer"
	viewer, err := h.client.User.Create().SetOidcSubject(viewerTok).SetEmail("viewer@test.local").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.Membership.Create().SetOrganizationID(orgID).SetUserID(viewer.ID).SetRole(membership.RoleViewer).Save(ctx); err != nil {
		t.Fatal(err)
	}
	runner, err := h.client.Cluster.Create().SetOrganizationID(orgID).SetName("runner").SetConnectionMethod(cluster.ConnectionMethodToken).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app, err := h.client.Application.Create().SetOrganizationID(orgID).SetName("web").SetRunnerClusterID(runner.ID).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/applications/" + app.ID.String() + "/workflow"

	infraStage, emptyStage, appsStage := uuid.New(), uuid.New(), uuid.New()
	infra, web := uuid.New(), uuid.New()
	body := func(webStage uuid.UUID, infraName string) string {
		stages := []map[string]any{
			{"id": infraStage, "name": infraName, "components": []map[string]any{{
				"id": infra, "name": "infra", "type": "terraform",
				"config": map[string]string{"repo_url": "https://example.com/infra.git", "path": ".", "backend": "s3", "backend_config": `{"bucket":"b","key":"k","region":"r"}`},
			}}},
			{"id": emptyStage, "name": "Later", "components": []map[string]any{}},
			{"id": appsStage, "name": "Apps", "components": []map[string]any{}},
		}
		webComp := map[string]any{
			"id": web, "name": "web", "type": "helm",
			"config":            map[string]string{"chart_source": "oci", "repo_url": "oci://example.com/charts/web", "values": "ns: ${{ components.infra.outputs.namespace }}"},
			"target_cluster_id": runner.ID, "target_namespace": "prod",
		}
		for _, st := range stages {
			if st["id"] == webStage {
				st["components"] = append(st["components"].([]map[string]any), webComp)
			}
		}
		b, _ := json.Marshal(map[string]any{"stages": stages})
		return string(b)
	}

	// The editor saves: Infrastructure (infra) → Later (empty) → Apps (web).
	rec := testReq{method: http.MethodPut, path: path, token: editorTok, orgID: orgID.String(), body: body(appsStage, " Infrastructure ")}.do(t, h.handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT workflow = %d\n%s", rec.Code, rec.Body.String())
	}
	var saved Workflow
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Stages) != 3 || saved.Stages[0].Name != "Infrastructure" || saved.Stages[1].Name != "Later" || saved.Stages[2].Name != "Apps" {
		t.Fatalf("saved stages = %+v", saved.Stages)
	}
	if len(saved.Stages[1].Components) != 0 || len(saved.Stages[2].Components) != 1 || saved.Stages[2].Components[0].Id != web {
		t.Fatalf("saved components = %+v", saved.Stages)
	}

	// The viewer reads the same layout with the inline values withheld.
	rec = testReq{method: http.MethodGet, path: path, token: viewerTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET workflow = %d\n%s", rec.Code, rec.Body.String())
	}
	var read Workflow
	if err := json.Unmarshal(rec.Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if len(read.Stages) != 3 || read.Stages[2].Components[0].Name != "web" {
		t.Fatalf("read stages = %+v", read.Stages)
	}
	if _, ok := read.Stages[2].Components[0].Config["values"]; ok {
		t.Error("viewer received the inline values")
	}

	// web referencing infra from the same stage is refused, as is a blank stage
	// name; neither changes what was saved.
	for name, c := range map[string]struct{ body, msg string }{
		"same-stage reference": {body(infraStage, "Infrastructure"), "earlier stage"},
		"blank stage name":     {body(appsStage, "  "), "needs a name"},
	} {
		rec = testReq{method: http.MethodPut, path: path, token: editorTok, orgID: orgID.String(), body: c.body}.do(t, h.handler)
		if rec.Code != http.StatusBadRequest || !strings.Contains(decodeErr(t, rec).Message, c.msg) {
			t.Errorf("%s: PUT = %d, want 400 mentioning %q\n%s", name, rec.Code, c.msg, rec.Body.String())
		}
	}
	if rec := (testReq{method: http.MethodPut, path: path, token: viewerTok, orgID: orgID.String(), body: body(appsStage, "x")}.do(t, h.handler)); rec.Code != http.StatusForbidden {
		t.Errorf("viewer PUT = %d, want 403", rec.Code)
	}
	stages, err := workflows.NewService(h.client).GetWorkflow(ctx, orgID, app.ID)
	if err != nil || len(stages) != 3 || stages[0].Name != "Infrastructure" || len(stages[2].Edges.Components) != 1 {
		t.Fatalf("workflow after refused saves = %+v (err %v)", stages, err)
	}

	// A run lists its stages (the empty one left out) on the run list and the
	// run detail.
	run, err := workflows.NewService(h.client).BeginRun(ctx, orgID, app.ID, workflows.ActionDeploy)
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	checkStages := func(where string, got *[]RunStage) {
		t.Helper()
		if got == nil || len(*got) != 2 {
			t.Fatalf("%s: stages = %+v, want 2", where, got)
		}
		st := *got
		if st[0].Name != "Infrastructure" || st[1].Name != "Apps" || st[0].Status != ComponentRunStatusPending {
			t.Errorf("%s: stages = %+v", where, st)
		}
		tf := st[0].Components
		if len(tf) != 1 || tf[0].ComponentId != infra || tf[0].Name != "infra" || len(tf[0].ComponentRunIds) != 2 {
			t.Errorf("%s: infra = %+v, want one component with plan + apply steps", where, tf)
		}
	}
	rec = testReq{method: http.MethodGet, path: "/api/applications/" + app.ID.String() + "/runs", token: viewerTok, orgID: orgID.String()}.do(t, h.handler)
	var list RunList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Runs) != 1 {
		t.Fatalf("run list = %s (err %v)", rec.Body.String(), err)
	}
	checkStages("list", list.Runs[0].Stages)
	rec = testReq{method: http.MethodGet, path: "/api/applications/" + app.ID.String() + "/runs/" + run.ID.String(), token: viewerTok, orgID: orgID.String()}.do(t, h.handler)
	var detail WorkflowRunDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("run detail: %v", err)
	}
	checkStages("detail", detail.Stages)
	if detail.Graph == nil || !strings.Contains(*detail.Graph, `"stages"`) {
		t.Errorf("run graph should record its stages: %v", detail.Graph)
	}
}
