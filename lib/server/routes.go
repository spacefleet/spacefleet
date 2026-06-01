package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"

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
	// entry wraps outermost. verifier is the bundled-Dex (OIDC) ID-token
	// verifier; if it is nil, RequireAuth fails closed (see lib/auth).
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

	// Bundled Dex, reverse-proxied same-origin under /dex so the app is the
	// single front door (the browser never talks to Dex directly). Mounted only
	// when an upstream is configured; route tests leave it unset.
	if cfg.DexUpstreamURL != "" {
		if h, err := dexProxyHandler(cfg.DexUpstreamURL); err != nil {
			log.Printf("dex proxy: not mounting /dex (%v)", err)
		} else {
			mux.Handle("/dex/", h)
		}
	}

	// Everything else is the SPA (or its static assets).
	mux.Handle("/", ui.Handler())
}

// dexProxyHandler builds a reverse proxy for the bundled Dex. Dex serves all
// of its routes under its issuer path (/dex), so requests pass straight through
// without rewriting — only the scheme/host are swapped to the upstream. The
// X-Forwarded-* headers are set for correctness, though Dex builds its URLs
// from its configured issuer rather than the inbound request.
func dexProxyHandler(upstream string) (http.Handler, error) {
	target, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("parse DEX_UPSTREAM_URL %q: %w", upstream, err)
	}
	if target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("DEX_UPSTREAM_URL %q must be an absolute URL", upstream)
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
		},
	}
	return proxy, nil
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
