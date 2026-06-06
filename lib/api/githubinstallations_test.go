package api

import (
	"net/http"
	"testing"
)

// TestCreateGitHubInstallationNilService proves the create handler reports a
// clear 503 (not a panic) when the github-installations service is unwired —
// the "services may be nil" contract. It needs no database, so it runs in the
// plain `go test` pass; the cross-org guard and happy path (which need real
// services) live in githubinstallations_integration_test.go.
func TestCreateGitHubInstallationNilService(t *testing.T) {
	// All services nil. resolveGitHubInstallationsWrite short-circuits on the nil
	// service before any membership/state work.
	handler := newTestHandler(ServerDeps{})

	rec := testReq{
		method: http.MethodPost,
		path:   "/api/github/installations",
		body:   `{"installation_id": 12345, "state": "anything"}`,
		token:  "alice",
		orgID:  "00000000-0000-0000-0000-000000000000",
	}.do(t, handler)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil service: got %d, want 503", rec.Code)
	}
	if e := decodeErr(t, rec); e.Code != "unavailable" {
		t.Fatalf("nil service: error code = %q, want unavailable", e.Code)
	}
}
