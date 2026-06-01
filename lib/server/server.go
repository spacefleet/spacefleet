package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/spacefleet/app/lib/auth"
	"github.com/spacefleet/app/lib/cache"
	"github.com/spacefleet/app/lib/clusters"
	"github.com/spacefleet/app/lib/config"
	"github.com/spacefleet/app/lib/db"
	"github.com/spacefleet/app/lib/organizations"
	"github.com/spacefleet/app/lib/secrets"
	"github.com/spacefleet/app/lib/users"
)

// New wires runtime dependencies (Postgres via ent, Redis) and returns a
// ready-to-serve *http.Server. Closing those dependencies is registered
// with Server.RegisterOnShutdown so callers only drive the HTTP lifecycle.
func New(cfg *config.Config) (*http.Server, error) {
	sqlDB, entClient, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Bounded connect timeout — if Redis is down at boot we want a clear
	// error, not a hanging process.
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	redisClient, err := cache.Open(pingCtx, cfg.RedisURL)
	if err != nil {
		_ = entClient.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("open redis: %w", err)
	}

	// Sealer for credentials stored at rest (e.g. cluster tokens/kubeconfigs).
	// An empty SecretKey yields a disabled sealer: features that store no
	// secrets keep working; storing a credential fails fast (see lib/secrets).
	sealer, err := secrets.NewSealer(cfg.SecretKey)
	if err != nil {
		_ = entClient.Close()
		_ = sqlDB.Close()
		_ = redisClient.Close()
		return nil, fmt.Errorf("build secret sealer: %w", err)
	}

	usersSvc := users.NewService(entClient)
	orgsSvc := organizations.NewService(entClient)
	clustersSvc := clusters.NewService(entClient, sealer)

	// Build the request authenticator. Spacefleet always authenticates against
	// its bundled Dex, so a configured OIDC issuer is mandatory — buildVerifier
	// errors (and boot fails) when it's missing, rather than degrading to an
	// allow-everyone mode (see lib/auth).
	verifier, err := buildVerifier(cfg)
	if err != nil {
		_ = entClient.Close()
		_ = sqlDB.Close()
		_ = redisClient.Close()
		return nil, fmt.Errorf("build auth verifier: %w", err)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           buildHandler(cfg, usersSvc, orgsSvc, clustersSvc, verifier),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	srv.RegisterOnShutdown(func() {
		_ = entClient.Close()
		_ = sqlDB.Close()
		_ = redisClient.Close()
	})
	return srv, nil
}

// buildHandler composes the full HTTP handler tree given pre-built deps.
// The services may be nil — the API surface returns a clear "not configured"
// error rather than panicking, which keeps route-level tests usable
// without a real database. verifier must be non-nil in production (built from
// the bundled-Dex OIDC config); a nil verifier makes RequireAuth fail closed.
func buildHandler(cfg *config.Config, usersSvc *users.Service, orgsSvc *organizations.Service, clustersSvc *clusters.Service, verifier auth.TokenVerifier) http.Handler {
	mux := http.NewServeMux()
	registerRoutes(mux, cfg, usersSvc, orgsSvc, clustersSvc, verifier)
	return logRequests(mux)
}

// buildVerifier returns the OIDC TokenVerifier for the bundled Dex. OIDC_ISSUER
// is mandatory — an empty issuer is a fatal misconfiguration, not a fallback to
// an unauthenticated mode. When OIDC_JWKS_URL is unset construction does OIDC
// discovery (network), so it's bounded by a timeout; when set, keys are fetched
// lazily and no startup network call runs.
func buildVerifier(cfg *config.Config) (auth.TokenVerifier, error) {
	if cfg.OIDCIssuer == "" {
		return nil, fmt.Errorf("OIDC_ISSUER is required: Spacefleet authenticates against its bundled Dex and has no unauthenticated mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return auth.NewOIDCVerifier(ctx, cfg.OIDCIssuer, cfg.OIDCClientID, cfg.OIDCJWKSURL)
}
