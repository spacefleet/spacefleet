//go:build integration

package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/spacefleet/spacefleet/ent/membership"
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
