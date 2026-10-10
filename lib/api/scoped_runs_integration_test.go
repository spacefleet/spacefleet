//go:build integration

package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/lib/testsupport"
)

// TestStartComponentRun covers the handler's own gates: a viewer is refused,
// a wrong action and a malformed target are 400s before anything else, and
// a valid request without a background worker is a 503 (the harness has no
// queue — the service and worker paths are covered in lib/workflows). The
// run mapping of a stored scope is checked through the run detail.
func TestStartComponentRun(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()

	editorTok, orgID := h.member("editor", membership.RoleEditor)
	const viewerTok = "viewer"
	viewer, err := h.client.User.Create().SetOidcSubject(viewerTok).SetEmail("viewer@test.local").Save(ctx)
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
		SetOrganizationID(orgID).SetApplicationID(app.ID).SetStageID(testsupport.Stage(t, h.client, orgID, app.ID)).SetName("infra").SetType("terraform").
		SetConfig(map[string]string{"repo_url": "r", "path": "p", "backend": "s3"}).Save(ctx)
	if err != nil {
		t.Fatalf("create component: %v", err)
	}
	path := "/api/applications/" + app.ID.String() + "/components/" + infra.ID.String() + "/runs"

	rec := testReq{method: http.MethodPost, path: path, token: viewerTok, orgID: orgID.String(), body: `{"action":"uninstall"}`}.do(t, h.handler)
	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer got %d, want 403\n%s", rec.Code, rec.Body.String())
	}
	rec = testReq{method: http.MethodPost, path: path, token: editorTok, orgID: orgID.String(), body: `{"action":"preview"}`}.do(t, h.handler)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("preview got %d, want 400\n%s", rec.Code, rec.Body.String())
	}
	rec = testReq{method: http.MethodPost, path: path, token: editorTok, orgID: orgID.String(), body: `{"action":"deploy","targets":["aws_instance.web;rm"]}`}.do(t, h.handler)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not a resource address") {
		t.Errorf("bad target got %d, want 400 naming the address\n%s", rec.Code, rec.Body.String())
	}
	rec = testReq{method: http.MethodPost, path: path, token: editorTok, orgID: orgID.String(), body: `{"action":"uninstall","targets":["aws_instance.web"]}`}.do(t, h.handler)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no worker got %d, want 503\n%s", rec.Code, rec.Body.String())
	}

	// A stored scoped run reads back with its scope; a whole-workflow run
	// has none.
	scoped, err := h.client.WorkflowRun.Create().
		SetOrganizationID(orgID).SetApplicationID(app.ID).SetAction("uninstall").
		SetArgs(`{"component_id":"` + infra.ID.String() + `","component_name":"infra","targets":["aws_instance.web"]}`).
		SetGraph(`{"nodes":[]}`).
		Save(ctx)
	if err != nil {
		t.Fatalf("create scoped run: %v", err)
	}
	whole, err := h.client.WorkflowRun.Create().
		SetOrganizationID(orgID).SetApplicationID(app.ID).SetAction("uninstall").SetGraph(`{"nodes":[]}`).
		Save(ctx)
	if err != nil {
		t.Fatalf("create whole run: %v", err)
	}
	rec = testReq{method: http.MethodGet, path: "/api/applications/" + app.ID.String() + "/runs/" + scoped.ID.String(), token: viewerTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("get run got %d\n%s", rec.Code, rec.Body.String())
	}
	// A run from before scopes recorded the component's type was OpenTofu.
	if body := rec.Body.String(); !strings.Contains(body, `"scope":{"component_id":"`+infra.ID.String()+`","component_name":"infra","component_type":"terraform","targets":["aws_instance.web"]}`) {
		t.Errorf("run detail lacks the scope:\n%s", body)
	}
	rec = testReq{method: http.MethodGet, path: "/api/applications/" + app.ID.String() + "/runs/" + whole.ID.String(), token: viewerTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"scope"`) {
		t.Errorf("whole-workflow run got %d / carries a scope\n%s", rec.Code, rec.Body.String())
	}
}
