//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/ent/componentrun"
	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// TestPolicies covers the handler gates and mapping: any member lists,
// only an admin writes, a Rego that does not compile is a 400 naming the
// problem, and a recorded verdict shows on a step.
func TestPolicies(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()

	adminTok, orgID := h.member("admin", membership.RoleAdmin)
	const editorTok = "editor"
	editor, err := h.client.User.Create().SetOidcSubject(editorTok).SetEmail("editor@test.local").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.Membership.Create().SetOrganizationID(orgID).SetUserID(editor.ID).SetRole(membership.RoleEditor).Save(ctx); err != nil {
		t.Fatal(err)
	}
	rego := "package spacefleet\\n\\ndeny contains msg if {\\n\\tinput.plan.destroy > 0\\n\\tmsg := \\\"no destroys\\\"\\n}\\n"

	rec := testReq{method: http.MethodPost, path: "/api/policies", token: editorTok, orgID: orgID.String(), body: `{"name":"p","rego":"` + rego + `"}`}.do(t, h.handler)
	if rec.Code != http.StatusForbidden {
		t.Errorf("editor create got %d, want 403", rec.Code)
	}
	rec = testReq{method: http.MethodPost, path: "/api/policies", token: adminTok, orgID: orgID.String(), body: `{"name":"p","rego":"package other\n"}`}.do(t, h.handler)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "package must be") {
		t.Errorf("wrong package got %d\n%s", rec.Code, rec.Body.String())
	}
	rec = testReq{method: http.MethodPost, path: "/api/policies", token: adminTok, orgID: orgID.String(), body: `{"name":"p","rego":"` + rego + `","enforcement":"warn"}`}.do(t, h.handler)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"enforcement":"warn"`) {
		t.Fatalf("create got %d\n%s", rec.Code, rec.Body.String())
	}
	id := extractID(t, rec.Body.Bytes())
	rec = testReq{method: http.MethodGet, path: "/api/policies", token: editorTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"p"`) {
		t.Errorf("list got %d\n%s", rec.Code, rec.Body.String())
	}
	rec = testReq{method: http.MethodPatch, path: "/api/policies/" + id, token: adminTok, orgID: orgID.String(), body: `{"enabled":false,"application_id":""}`}.do(t, h.handler)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Errorf("update got %d\n%s", rec.Code, rec.Body.String())
	}
	rec = testReq{method: http.MethodDelete, path: "/api/policies/" + id, token: adminTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete got %d\n%s", rec.Code, rec.Body.String())
	}
}

// TestPolicyDryRun proves the dry run: recent succeeded plan steps are
// listed (plan units only — the apply unit and a failed plan are left out)
// with their counts, a policy evaluated against one reports the messages it
// would have produced plus the input it saw, a non-compiling policy is a
// 400, an apply step is a 404, and editors get neither endpoint.
func TestPolicyDryRun(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	adminTok, orgID := h.member("admin", membership.RoleAdmin)
	const editorTok = "editor"
	editor, err := h.client.User.Create().SetOidcSubject(editorTok).SetEmail("editor@test.local").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.Membership.Create().SetOrganizationID(orgID).SetUserID(editor.ID).SetRole(membership.RoleEditor).Save(ctx); err != nil {
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
	infra, err := h.client.Component.Create().SetOrganizationID(orgID).SetApplicationID(app.ID).SetName("infra").SetType("terraform").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	run, err := h.client.WorkflowRun.Create().SetOrganizationID(orgID).SetApplicationID(app.ID).SetAction("deploy").SetStartedBy("kyle@example.com").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	planLogs := "OpenTofu will perform the following actions:\n\n  # aws_db_instance.main will be destroyed\n  - resource \"aws_db_instance\" \"main\" {\n    }\n\n  # aws_instance.web will be created\n  + resource \"aws_instance\" \"web\" {\n    }\n\nPlan: 1 to add, 0 to change, 1 to destroy.\n"
	seed := func(componentID uuid.UUID, name string, status componentrun.Status, logs string) uuid.UUID {
		cr, err := h.client.ComponentRun.Create().
			SetOrganizationID(orgID).SetWorkflowRunID(run.ID).SetComponentID(componentID).SetName(name).SetType("terraform").
			SetStatus(status).SetLogs(logs).SetFinishedAt(time.Now()).Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return cr.ID
	}
	planID := seed(infra.ID, "infra · plan", componentrun.StatusSucceeded, planLogs)
	applyID := seed(workflows.DeriveApplyID(infra.ID), "infra · apply", componentrun.StatusSucceeded, "Apply complete!")
	seed(infra.ID, "infra · plan", componentrun.StatusFailed, "Error: boom")
	// A plan unit is recognised by its own snapshot name, so a plan of a
	// component since removed from the workflow stays available (component
	// runs keep no FK to components).
	if err := h.client.Component.DeleteOneID(infra.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}

	// Editors get neither endpoint.
	rec := testReq{method: http.MethodGet, path: "/api/policies/plans", token: editorTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusForbidden {
		t.Errorf("editor plans got %d, want 403", rec.Code)
	}
	rec = testReq{method: http.MethodPost, path: "/api/policies/test", token: editorTok, orgID: orgID.String(), body: `{"rego":"package spacefleet\n","component_run_id":"` + planID.String() + `"}`}.do(t, h.handler)
	if rec.Code != http.StatusForbidden {
		t.Errorf("editor test got %d, want 403", rec.Code)
	}

	// The plan picker: the one succeeded plan unit, with its counts.
	rec = testReq{method: http.MethodGet, path: "/api/policies/plans", token: adminTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("plans got %d\n%s", rec.Code, rec.Body.String())
	}
	var plans []PolicyTestPlan
	if err := json.Unmarshal(rec.Body.Bytes(), &plans); err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].ComponentRunId != planID || plans[0].ComponentName != "infra" || plans[0].ApplicationName != "web" || plans[0].Add != 1 || plans[0].Destroy != 1 {
		t.Fatalf("plans = %+v", plans)
	}

	// The dry run: a policy that denies destroys of databases fires on this
	// plan and reports what it saw; one that never fires passes.
	rego := "package spacefleet\n\ndeny contains msg if {\n\tsome r in input.plan.resources\n\tr.action == \"delete\"\n\tstartswith(r.type, \"aws_db\")\n\tmsg := sprintf(\"%s would be destroyed\", [r.address])\n}\n"
	body, _ := json.Marshal(map[string]any{"rego": rego, "component_run_id": planID})
	rec = testReq{method: http.MethodPost, path: "/api/policies/test", token: adminTok, orgID: orgID.String(), body: string(body)}.do(t, h.handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("test got %d\n%s", rec.Code, rec.Body.String())
	}
	var result PolicyTestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Violations) != 1 || result.Violations[0] != "aws_db_instance.main would be destroyed" || result.Error != nil {
		t.Errorf("result = %+v", result)
	}
	if runIn, _ := result.Input["run"].(map[string]any); runIn["started_by"] != "kyle@example.com" || runIn["action"] != "deploy" {
		t.Errorf("input.run = %v", result.Input["run"])
	}
	body, _ = json.Marshal(map[string]any{"rego": "package spacefleet\n\ndeny contains msg if {\n\tinput.plan.destroy > 5\n\tmsg := \"too many\"\n}\n", "component_run_id": planID})
	rec = testReq{method: http.MethodPost, path: "/api/policies/test", token: adminTok, orgID: orgID.String(), body: string(body)}.do(t, h.handler)
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || rec.Code != http.StatusOK || len(result.Violations) != 0 {
		t.Errorf("passing policy: %d %s (err %v)", rec.Code, rec.Body.String(), err)
	}

	// Bad Rego is a 400 with the compiler's message; an apply unit is a 404.
	body, _ = json.Marshal(map[string]any{"rego": "package other\n", "component_run_id": planID})
	rec = testReq{method: http.MethodPost, path: "/api/policies/test", token: adminTok, orgID: orgID.String(), body: string(body)}.do(t, h.handler)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "package must be") {
		t.Errorf("bad rego got %d\n%s", rec.Code, rec.Body.String())
	}
	body, _ = json.Marshal(map[string]any{"rego": rego, "component_run_id": applyID})
	rec = testReq{method: http.MethodPost, path: "/api/policies/test", token: adminTok, orgID: orgID.String(), body: string(body)}.do(t, h.handler)
	if rec.Code != http.StatusNotFound {
		t.Errorf("apply unit got %d, want 404\n%s", rec.Code, rec.Body.String())
	}
}
