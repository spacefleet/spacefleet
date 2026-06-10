package api

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A request the generated code can't decode must come back as a JSON Error,
// not the oapi-codegen default of http.Error text/plain. The decode detail is
// derived from the caller's own input, so echoing it is fine.
func TestRequestErrorsAreJSONBadRequest(t *testing.T) {
	handler := newTestHandler(ServerDeps{})

	rec := testReq{method: http.MethodPost, path: "/api/organizations", body: `{"name":`, token: "user-1"}.do(t, handler)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	e := decodeErr(t, rec)
	if e.Code != "bad_request" {
		t.Errorf("code = %q, want %q", e.Code, "bad_request")
	}
	if e.Message == "" {
		t.Error("message is empty; want the decode detail so the caller can fix the request")
	}
}

// A handler error no case mapped to a typed response must be logged
// server-side and answered with an opaque JSON 500 — never echoed to the
// client (the generated default sends raw ent/pgx/k8s error strings).
func TestUnhandledHandlerErrorsAreOpaqueJSON500(t *testing.T) {
	// Force the response-error path through a real generated operation: a
	// strict middleware that fails every call with an internal-looking error,
	// standing in for the handlers' unmapped `return nil, err` branches.
	fail := func(f StrictHandlerFunc, operationID string) StrictHandlerFunc {
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
			return nil, errors.New(`pq: duplicate key value violates unique constraint "memberships_pkey"`)
		}
	}

	mux := http.NewServeMux()
	HandlerWithOptions(
		NewStrictHandlerWithOptions(NewServer(ServerDeps{}), []StrictMiddlewareFunc{fail}, StrictErrorOptions()),
		StdHTTPServerOptions{BaseRouter: mux},
	)

	var logged bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(prev)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	e := decodeErr(t, rec)
	if e.Code != "internal" {
		t.Errorf("code = %q, want %q", e.Code, "internal")
	}
	if body := rec.Body.String(); strings.Contains(body, "pq:") || strings.Contains(body, "memberships_pkey") {
		t.Errorf("internal error detail leaked to the client: %q", body)
	}
	if !strings.Contains(logged.String(), "memberships_pkey") {
		t.Errorf("unhandled error was not logged server-side: %q", logged.String())
	}
}
