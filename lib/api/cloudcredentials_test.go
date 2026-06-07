package api

import (
	"net/http"
	"testing"
)

// TestCloudCredentialNilService proves the handlers report a clear 503 (not a
// panic) when the cloud-credentials service is unwired — the "services may be
// nil" contract. It needs no database, so it runs in the plain `go test` pass;
// the role gate, provider validation, and happy path (which need real services)
// live in cloudcredentials_integration_test.go.
func TestCloudCredentialNilService(t *testing.T) {
	handler := newTestHandler(ServerDeps{}) // every service nil
	orgID := "00000000-0000-0000-0000-000000000000"

	cases := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/cloud-credentials", ""},
		{http.MethodPost, "/api/cloud-credentials", `{"name":"x","provider":"aws"}`},
		{http.MethodGet, "/api/cloud-credentials/" + orgID, ""},
		{http.MethodPatch, "/api/cloud-credentials/" + orgID, `{"name":"y"}`},
		{http.MethodDelete, "/api/cloud-credentials/" + orgID, ""},
	}
	for _, c := range cases {
		rec := testReq{method: c.method, path: c.path, body: c.body, token: "alice", orgID: orgID}.do(t, handler)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: got %d, want 503", c.method, c.path, rec.Code)
		}
		if e := decodeErr(t, rec); e.Code != "unavailable" {
			t.Fatalf("%s %s: error code = %q, want unavailable", c.method, c.path, e.Code)
		}
	}
}
