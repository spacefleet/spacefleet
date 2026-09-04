//go:build integration

package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/ent/membership"
)

// TestSetClusterTektonPluginCache covers the handler's own gates: a viewer is
// refused, a malformed size is a 400 before any cluster call, and another
// org's cluster is a 404. The cluster-touching path is covered by lib/tekton's
// claim tests behind the clusters service seam.
func TestSetClusterTektonPluginCache(t *testing.T) {
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
		t.Fatalf("create cluster: %v", err)
	}
	path := "/api/clusters/" + runner.ID.String() + "/tekton/plugin-cache"

	rec := testReq{method: http.MethodPut, path: path, body: `{"size":"20Gi"}`, token: "viewer", orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer got %d, want 403\n%s", rec.Code, rec.Body.String())
	}
	for _, body := range []string{`{"size":"lots"}`, `{"size":"-1Gi"}`} {
		rec = testReq{method: http.MethodPut, path: path, body: body, token: editorTok, orgID: orgID.String()}.do(t, h.handler)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: got %d, want 400\n%s", body, rec.Code, rec.Body.String())
		}
	}
	otherTok, otherOrg := h.member("other", membership.RoleEditor)
	rec = testReq{method: http.MethodPut, path: path, body: `{"size":"20Gi"}`, token: otherTok, orgID: otherOrg.String()}.do(t, h.handler)
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-org got %d, want 404\n%s", rec.Code, rec.Body.String())
	}
}
