package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spacefleet/spacefleet/lib/config"
	"github.com/spacefleet/spacefleet/lib/testsupport"
)

// handler builds the HTTP tree without a real Postgres/Redis. The account
// services are nil, so their handlers return 503 — enough to prove the API is
// mounted under the auth middleware and the SPA fallback works.
func handler() http.Handler {
	// A fake verifier stands in for Dex so requests reach the handlers (this
	// proves routing, not auth — the server has no passthrough mode).
	return buildHandler(&config.Config{Addr: ":0", Env: "test"}, nil, nil, nil, testsupport.FakeVerifier())
}

func TestHealthEndpointIsPublic(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("expected status=ok, got %q", body.Status)
	}
}

// TestMeRouteMounted confirms /api/me is wired through the generated handler.
// With a nil account service it returns 503 — which also proves the fake
// verifier let the request reach the handler (rather than 401).
func TestMeRouteMounted(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	rec := httptest.NewRecorder()
	handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 from the nil account service, got %d", rec.Code)
	}
}

func TestSPAFallback(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/some/spa/route", nil)
	rec := httptest.NewRecorder()
	handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" || ct[:9] != "text/html" {
		t.Fatalf("expected html content-type, got %q", ct)
	}
}
