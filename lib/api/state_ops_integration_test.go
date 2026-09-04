//go:build integration

package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/ent/membership"
)

// TestStartStateOperation covers the handler's own gates: a viewer is
// refused, a malformed operation is a 400 before anything else, and a valid
// request without a background worker is a 503 (the harness has no queue —
// the service and worker paths are covered in lib/workflows). The run-detail
// mapping of a stored operation is checked through the runs list.
func TestStartStateOperation(t *testing.T) {
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
		SetOrganizationID(orgID).SetApplicationID(app.ID).SetName("infra").SetType("terraform").
		SetConfig(map[string]string{"repo_url": "r", "path": "p", "backend": "s3", "plan_flags": `["-var=env=prod","-target=x"]`}).Save(ctx)
	if err != nil {
		t.Fatalf("create component: %v", err)
	}
	path := "/api/applications/" + app.ID.String() + "/components/" + infra.ID.String() + "/state-ops"

	rec := testReq{method: http.MethodPost, path: path, token: viewerTok, orgID: orgID.String(), body: `{"operation":"rm","address":"aws_instance.web"}`}.do(t, h.handler)
	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer got %d, want 403\n%s", rec.Code, rec.Body.String())
	}
	rec = testReq{method: http.MethodPost, path: path, token: editorTok, orgID: orgID.String(), body: `{"operation":"mv","address":"a"}`}.do(t, h.handler)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "requires new_address") {
		t.Errorf("incomplete mv got %d, want 400 naming the field\n%s", rec.Code, rec.Body.String())
	}
	rec = testReq{method: http.MethodPost, path: path, token: editorTok, orgID: orgID.String(), body: `{"operation":"rm","address":"aws_instance.web"}`}.do(t, h.handler)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no worker got %d, want 503\n%s", rec.Code, rec.Body.String())
	}

	// A stored state_op run lists with its operation and the exact command,
	// the import carrying only the component's -var plan flag.
	wr, err := h.client.WorkflowRun.Create().
		SetOrganizationID(orgID).SetApplicationID(app.ID).SetAction("state_op").
		SetArgs(`{"operation":"import","address":"aws_s3_bucket.data","import_id":"acme-data"}`).
		SetGraph(`{"nodes":[{"id":"` + infra.ID.String() + `","component_id":"` + infra.ID.String() + `","name":"infra · import","type":"terraform","config":{"command":"state_op","plan_flags":"[\"-var=env=prod\",\"-target=x\"]"},"depends_on":[],"requires_approval":true}]}`).
		Save(ctx)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	rec = testReq{method: http.MethodGet, path: "/api/applications/" + app.ID.String() + "/runs/" + wr.ID.String(), token: viewerTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("get run got %d\n%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"command":"tofu import -input=false -no-color -var=env=prod aws_s3_bucket.data acme-data"`) || !strings.Contains(body, `"operation":"import"`) {
		t.Errorf("run detail lacks the state operation + command:\n%s", body)
	}
}
