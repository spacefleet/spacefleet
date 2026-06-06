package api

import (
	"net/http"
	"testing"
)

// The SSE handlers in stream.go are mounted raw on the mux (they bypass
// oapi-codegen), so they're tested by serving the handler tree directly. These
// pre-stream nil-service paths need no database — they short-circuit in the
// resolve preamble — so they run in the plain `go test` pass. The not-found /
// 409 classifications and the snapshot-then-close happy path (which need real
// rows) live in stream_integration_test.go.

const zeroUUID = "00000000-0000-0000-0000-000000000000"

// TestStreamNilServiceReturns503 proves each SSE handler reports a clear 503
// (rather than panicking or opening a half-stream) when its backing service is
// unwired. writeStreamError renders this as a normal JSON Error before any
// event-stream headers are written.
func TestStreamNilServiceReturns503(t *testing.T) {
	handler := newTestHandler(ServerDeps{}) // every service nil

	cases := []struct {
		name string
		path string
	}{
		{"cluster nodes (nil clusters)", "/api/clusters/" + zeroUUID + "/nodes/stream"},
		{"cluster tekton (nil clusters)", "/api/clusters/" + zeroUUID + "/tekton/stream"},
		{"tekton run (nil clusters)", "/api/clusters/" + zeroUUID + "/tekton/runs/build/stream"},
		{"application run (nil workflows)", "/api/applications/" + zeroUUID + "/runs/" + zeroUUID + "/stream"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := testReq{
				method: http.MethodGet,
				path:   c.path,
				token:  "alice",
				orgID:  zeroUUID,
			}.do(t, handler)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("got %d, want 503\n%s", rec.Code, rec.Body.String())
			}
			// The error is rendered as JSON, not an event-stream.
			if ct := rec.Header().Get("Content-Type"); ct == "" || ct[:16] != "application/json" {
				t.Fatalf("content-type = %q, want application/json", ct)
			}
			if e := decodeErr(t, rec); e.Code != "unavailable" {
				t.Fatalf("error code = %q, want unavailable", e.Code)
			}
		})
	}
}
