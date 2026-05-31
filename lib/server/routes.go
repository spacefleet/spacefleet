package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spacefleet/app/lib/api"
	"github.com/spacefleet/app/lib/auth"
	"github.com/spacefleet/app/lib/clusters"
	"github.com/spacefleet/app/lib/config"
	"github.com/spacefleet/app/lib/organizations"
	"github.com/spacefleet/app/lib/users"
	"github.com/spacefleet/app/ui"
)

// publicAPIPaths are the /api/* paths that skip authentication entirely.
var publicAPIPaths = []string{
	"/api/health",
}

func registerRoutes(mux *http.ServeMux, cfg *config.Config, usersSvc *users.Service, orgsSvc *organizations.Service, clustersSvc *clusters.Service, verifier auth.TokenVerifier) {
	// API routes are generated from api/openapi.yaml and mounted under
	// /api/*. oapi-codegen applies middlewares in reverse, so the last
	// entry wraps outermost. verifier is the Dex (OIDC) ID-token verifier
	// when OIDC_ISSUER is configured; when nil, RequireAuth falls back to
	// the dev passthrough (see lib/auth).
	api.HandlerWithOptions(api.NewStrictHandler(api.NewServer(usersSvc, orgsSvc, clustersSvc), nil), api.StdHTTPServerOptions{
		BaseRouter: mux,
		Middlewares: []api.MiddlewareFunc{
			// OrgContext lifts X-Organization-ID onto the request context for
			// org-scoped handlers. RequireAuth is listed last so it wraps
			// outermost (oapi-codegen applies middlewares in reverse) — auth
			// runs first, then org resolution.
			api.MiddlewareFunc(auth.OrgContext),
			api.MiddlewareFunc(auth.RequireAuth(publicAPIPaths, verifier)),
		},
	})

	// Public config exposed to the browser as `window.appConfig`. Only
	// pre-approved, non-secret values go here — it ships to every client.
	mux.HandleFunc("/config.js", appConfigHandler(cfg))

	// Everything else is the SPA (or its static assets).
	mux.Handle("/", ui.Handler())
}

func appConfigHandler(cfg *config.Config) http.HandlerFunc {
	payload, err := json.Marshal(map[string]string{
		// OIDC values the SPA will need for its Dex login flow. Empty until
		// configured; both are non-secret.
		"oidcIssuer":   cfg.OIDCIssuer,
		"oidcClientId": cfg.OIDCClientID,
	})
	if err != nil {
		panic(err)
	}
	body := fmt.Sprintf("window.appConfig=%s;", payload)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(body))
	}
}
