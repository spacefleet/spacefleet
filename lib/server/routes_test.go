package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacefleet/spacefleet/lib/api"
	"github.com/spacefleet/spacefleet/lib/config"
	"github.com/spacefleet/spacefleet/lib/testsupport"
)

// handler builds the HTTP tree without a real Postgres. The account
// services are nil, so their handlers return 503 — enough to prove the API is
// mounted under the auth middleware and the SPA fallback works.
func handler() http.Handler {
	// A fake verifier stands in for Dex so requests reach the handlers (this
	// proves routing, not auth — the server has no passthrough mode). All
	// dependencies are zero/nil, so handlers report "not configured".
	return buildHandler(&config.Config{Addr: ":0", Env: "test"}, api.ServerDeps{}, testsupport.FakeVerifier())
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

// TestClusterCapabilitiesRouteMounted confirms GET
// /api/clusters/{id}/capabilities is wired through the generated handler. With a
// nil clusters service it returns 503 (the resolveOrg preamble), which also
// proves the fake verifier let the request reach the handler rather than 401.
func TestClusterCapabilitiesRouteMounted(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/clusters/00000000-0000-0000-0000-000000000000/capabilities", nil)
	rec := httptest.NewRecorder()
	handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 from the nil clusters service, got %d", rec.Code)
	}
}

// TestGenerateClusterRbacRouteMounted confirms POST
// /api/clusters/{id}/capabilities/rbac is wired through the generated handler.
// A valid body reaches resolveOrg, where the nil clusters service yields 503 —
// which also proves the fake verifier let the request through rather than 401.
func TestGenerateClusterRbacRouteMounted(t *testing.T) {
	body := strings.NewReader(`{"keys":["view_pods"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/clusters/00000000-0000-0000-0000-000000000000/capabilities/rbac", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 from the nil clusters service, got %d", rec.Code)
	}
}

// TestAppConfigHandler proves /config.js emits the non-secret values the SPA
// needs as a window.appConfig assignment — including the login methods, which
// drive the login screen's per-connector buttons.
func TestAppConfigHandler(t *testing.T) {
	cfg := &config.Config{
		OIDCIssuer:       "https://app.example.com/dex",
		OIDCClientID:     "spacefleet",
		AllowOrgCreation: true,
		LoginMethods: []config.LoginMethod{
			{ID: "github", Name: "GitHub", Type: "github"},
			{ID: "local", Name: "Email and password", Type: "password"},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/config.js", nil)
	rec := httptest.NewRecorder()
	appConfigHandler(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
		t.Fatalf("expected javascript content-type, got %q", ct)
	}

	// Strip the `window.appConfig=...;` wrapper to get back the JSON payload.
	body := rec.Body.String()
	jsonStr := strings.TrimSuffix(strings.TrimPrefix(body, "window.appConfig="), ";")

	var got struct {
		OIDCIssuer   string               `json:"oidcIssuer"`
		OIDCClientID string               `json:"oidcClientId"`
		LoginMethods []config.LoginMethod `json:"loginMethods"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &got); err != nil {
		t.Fatalf("decode appConfig payload %q: %v", jsonStr, err)
	}

	if got.OIDCIssuer != cfg.OIDCIssuer || got.OIDCClientID != cfg.OIDCClientID {
		t.Fatalf("oidc values: got issuer=%q clientId=%q", got.OIDCIssuer, got.OIDCClientID)
	}
	if len(got.LoginMethods) != 2 {
		t.Fatalf("expected 2 login methods, got %d (%v)", len(got.LoginMethods), got.LoginMethods)
	}
	if got.LoginMethods[0].ID != "github" || got.LoginMethods[1].Type != "password" {
		t.Fatalf("login methods did not round-trip: %v", got.LoginMethods)
	}
}

// TestAppConfigHandlerEmptyLoginMethods proves an unset list serializes as an
// empty JSON array (not null), so the SPA can safely fall back to its generic
// "Sign in" button.
func TestAppConfigHandlerEmptyLoginMethods(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/config.js", nil)
	rec := httptest.NewRecorder()
	appConfigHandler(&config.Config{LoginMethods: []config.LoginMethod{}}).ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), `"loginMethods":[]`) {
		t.Fatalf("expected loginMethods to serialize as [], got %q", rec.Body.String())
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

// TestGitHubWebhookRouteIsPublic confirms POST /api/webhooks/github is mounted
// outside the auth chain: with no token it reaches the handler, which
// reports 503 (no webhook secret configured) rather than 401.
func TestGitHubWebhookRouteIsPublic(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/github", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 from the unconfigured webhook, got %d", rec.Code)
	}
}

// TestTofuStateRoutesArePublic confirms the managed OpenTofu state routes are
// mounted outside the Dex auth chain (runner pods authenticate with a state
// token, not a user token): with no bearer they reach the handler, which
// reports 503 (managed state not configured here) rather than 401. A
// LOCK-verb request is not routed at all — locks use POST/DELETE.
func TestTofuStateRoutesArePublic(t *testing.T) {
	path := "/api/tofu/state/0b6f8b6e-6c47-4a6e-9d0b-1f4f7a3c2b10/default"
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, path},
		{http.MethodPost, path},
		{http.MethodPost, path + "/lock"},
		{http.MethodDelete, path + "/lock"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: expected 503 from the unconfigured state handler, got %d", tc.method, tc.path, rec.Code)
		}
	}
}

// TestAppConfigManagedState: /config.js says whether managed OpenTofu state
// is available (a secret key is set) — the boolean only, never the key.
func TestAppConfigManagedState(t *testing.T) {
	for key, want := range map[string]string{"": `"managedStateEnabled":false`, "c2VjcmV0": `"managedStateEnabled":true`} {
		rec := httptest.NewRecorder()
		appConfigHandler(&config.Config{SecretKey: key, LoginMethods: []config.LoginMethod{}}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/config.js", nil))
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("key %q: want %s in %s", key, want, rec.Body.String())
		}
		if key != "" && strings.Contains(rec.Body.String(), key) {
			t.Errorf("config.js leaks the secret key")
		}
	}
}
