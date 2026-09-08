//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/spacefleet/spacefleet/ent/membership"
)

// TestNotificationChannels covers the handler gates and mapping: any member
// lists, only an admin writes, validation is a 400, the sealed URL is never
// returned, and a test send without a worker is a 503.
func TestNotificationChannels(t *testing.T) {
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

	rec := testReq{method: http.MethodPost, path: "/api/notification-channels", token: editorTok, orgID: orgID.String(), body: `{"name":"ops","kind":"email","target":"ops@example.com","events":["run_failed"]}`}.do(t, h.handler)
	if rec.Code != http.StatusForbidden {
		t.Errorf("editor create got %d, want 403\n%s", rec.Code, rec.Body.String())
	}
	rec = testReq{method: http.MethodPost, path: "/api/notification-channels", token: adminTok, orgID: orgID.String(), body: `{"name":"ops","kind":"email","target":"not-an-email","events":["run_failed"]}`}.do(t, h.handler)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad target got %d, want 400\n%s", rec.Code, rec.Body.String())
	}
	rec = testReq{method: http.MethodPost, path: "/api/notification-channels", token: adminTok, orgID: orgID.String(), body: `{"name":"ops","kind":"email","target":"ops@example.com","events":["run_failed","drift_detected"]}`}.do(t, h.handler)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create got %d\n%s", rec.Code, rec.Body.String())
	}
	id := extractID(t, rec.Body.Bytes())
	rec = testReq{method: http.MethodPost, path: "/api/notification-channels", token: adminTok, orgID: orgID.String(), body: `{"name":"slack","kind":"slack","target":"https://hooks.slack.com/services/T/B/secret","events":["awaiting_approval"]}`}.do(t, h.handler)
	if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "secret") || !strings.Contains(rec.Body.String(), `"address":"hooks.slack.com"`) {
		t.Errorf("slack create got %d / leaks the URL\n%s", rec.Code, rec.Body.String())
	}

	rec = testReq{method: http.MethodGet, path: "/api/notification-channels", token: editorTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ops@example.com"`) || strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("list got %d\n%s", rec.Code, rec.Body.String())
	}
	rec = testReq{method: http.MethodPatch, path: "/api/notification-channels/" + id, token: adminTok, orgID: orgID.String(), body: `{"events":["awaiting_approval"],"application_id":""}`}.do(t, h.handler)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"events":["awaiting_approval"]`) {
		t.Errorf("update got %d\n%s", rec.Code, rec.Body.String())
	}
	rec = testReq{method: http.MethodPost, path: "/api/notification-channels/" + id + "/test", token: adminTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("test without worker got %d, want 503\n%s", rec.Code, rec.Body.String())
	}
	rec = testReq{method: http.MethodDelete, path: "/api/notification-channels/" + id, token: editorTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusForbidden {
		t.Errorf("editor delete got %d, want 403", rec.Code)
	}
	rec = testReq{method: http.MethodDelete, path: "/api/notification-channels/" + id, token: adminTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete got %d\n%s", rec.Code, rec.Body.String())
	}
}

// extractID reads the id of a created resource from its JSON body.
func extractID(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		Id string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Id == "" {
		t.Fatalf("no id in %s (err %v)", body, err)
	}
	return out.Id
}
