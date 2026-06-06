package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacefleet/spacefleet/lib/auth"
	"github.com/spacefleet/spacefleet/lib/testsupport"
)

// This file is the single, shared handler test harness for package api. It is
// deliberately the ONE place test helpers live so the unit (`go test`) and
// integration (`go test -tags=integration`) runs both compile without
// duplicate-symbol clashes. DB-backed fixtures that need a real *ent.Client
// live in harness_integration_test.go behind the integration build tag; the
// helpers here have no database dependency and run in every test pass.
//
// It exists because clusters_rbac_test.go and authz_test.go are pure-helper
// unit tests — there was no server-level harness before this.

// testPublicPaths mirrors the server's publicAPIPaths (only /api/health bypasses
// auth). The harness re-declares it rather than importing lib/server to avoid an
// import cycle (lib/server already imports lib/api).
var testPublicPaths = []string{"/api/health"}

// newTestHandler builds the HTTP tree for the given dependencies, wired exactly
// like lib/server/routes.go: the generated /api/* handlers behind
// OrgContext+RequireAuth, plus the raw SSE stream routes behind the same chain.
// A FakeVerifier stands in for Dex so a request's Authorization bearer is taken
// verbatim as the user's OIDC subject (a distinct token => a distinct user).
//
// Any nil service in deps makes its handlers report "not configured" (503),
// which is exactly the behavior several tests assert; non-nil services (built by
// the integration harness) exercise the real resolve/authorize path.
func newTestHandler(deps ServerDeps) http.Handler {
	srv := NewServer(deps)
	verifier := testsupport.FakeVerifier()

	mux := http.NewServeMux()

	HandlerWithOptions(NewStrictHandler(srv, nil), StdHTTPServerOptions{
		BaseRouter: mux,
		Middlewares: []MiddlewareFunc{
			MiddlewareFunc(auth.OrgContext),
			MiddlewareFunc(auth.RequireAuth(testPublicPaths, verifier)),
		},
	})

	// Raw SSE routes (bypass oapi-codegen), behind the same auth chain.
	stream := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(testPublicPaths, verifier)(auth.OrgContext(h))
	}
	mux.Handle("GET /api/clusters/{id}/nodes/stream", stream(srv.StreamClusterNodes))
	mux.Handle("GET /api/clusters/{id}/tekton/stream", stream(srv.StreamClusterTekton))
	mux.Handle("GET /api/clusters/{id}/tekton/runs/{name}/stream", stream(srv.StreamClusterTektonRun))
	mux.Handle("GET /api/applications/{id}/runs/{runId}/stream", stream(srv.StreamApplicationRun))

	return mux
}

// testReq is a small builder for an authenticated, org-scoped request against
// the harness. token becomes the user's OIDC subject (empty => "test-user");
// orgID is sent as X-Organization-ID (empty => header omitted).
type testReq struct {
	method string
	path   string
	body   string
	token  string
	orgID  string
}

// do executes the request against handler and returns the recorder.
func (tr testReq) do(t *testing.T, handler http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if tr.body != "" {
		req = httptest.NewRequest(tr.method, tr.path, strings.NewReader(tr.body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(tr.method, tr.path, nil)
	}
	if tr.token != "" {
		req.Header.Set("Authorization", "Bearer "+tr.token)
	}
	if tr.orgID != "" {
		req.Header.Set("X-Organization-ID", tr.orgID)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// decodeErr decodes a JSON Error body the typed/stream handlers emit.
func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) Error {
	t.Helper()
	var e Error
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode Error body %q: %v", rec.Body.String(), err)
	}
	return e
}
