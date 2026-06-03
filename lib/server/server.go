package server

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"time"

	"github.com/spacefleet/spacefleet/lib/api"
	"github.com/spacefleet/spacefleet/lib/auth"
	"github.com/spacefleet/spacefleet/lib/clusters"
	"github.com/spacefleet/spacefleet/lib/config"
	"github.com/spacefleet/spacefleet/lib/db"
	"github.com/spacefleet/spacefleet/lib/invitations"
	"github.com/spacefleet/spacefleet/lib/organizations"
	"github.com/spacefleet/spacefleet/lib/queue"
	"github.com/spacefleet/spacefleet/lib/secrets"
	"github.com/spacefleet/spacefleet/lib/users"
)

// New wires runtime dependencies (Postgres via ent) and returns a
// ready-to-serve *http.Server. Closing those dependencies is registered
// with Server.RegisterOnShutdown so callers only drive the HTTP lifecycle.
func New(cfg *config.Config) (*http.Server, error) {
	sqlDB, entClient, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Sealer for credentials stored at rest (e.g. cluster tokens/kubeconfigs).
	// An empty SecretKey yields a disabled sealer: features that store no
	// secrets keep working; storing a credential fails fast (see lib/secrets).
	sealer, err := secrets.NewSealer(cfg.SecretKey)
	if err != nil {
		_ = entClient.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("build secret sealer: %w", err)
	}

	usersSvc := users.NewService(entClient)
	orgsSvc := organizations.NewService(entClient)
	clustersSvc := clusters.NewService(entClient, sealer)
	invitesSvc := invitations.NewService(entClient)

	// Build the request authenticator. Spacefleet always authenticates against
	// its bundled Dex, so a configured OIDC issuer is mandatory — buildVerifier
	// errors (and boot fails) when it's missing, rather than degrading to an
	// allow-everyone mode (see lib/auth).
	verifier, err := buildVerifier(cfg)
	if err != nil {
		_ = entClient.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("build auth verifier: %w", err)
	}

	// Insert-only River client for enqueueing background jobs (invitation emails,
	// Tekton installs). Best-effort: if the pool can't open, the API still works —
	// invites just return a copy-able link with no email sent, and a Tekton enable
	// that needs the worker returns 503. The worker process owns the actual job
	// execution and its own migrations.
	jobQueue, closeQueue := buildJobQueue(cfg)

	deps := api.ServerDeps{
		Users:            usersSvc,
		Orgs:             orgsSvc,
		Clusters:         clustersSvc,
		Invites:          invitesSvc,
		AllowOrgCreation: cfg.AllowOrgCreation,
		ExternalURL:      cfg.ExternalURL,
		EmailEnabled:     cfg.EmailEnabled(),
		JobQueue:         jobQueue,
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           buildHandler(cfg, deps, verifier),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	srv.RegisterOnShutdown(func() {
		_ = entClient.Close()
		_ = sqlDB.Close()
		closeQueue()
	})
	return srv, nil
}

// buildJobQueue opens an insert-only River client for enqueueing background
// jobs. It's best-effort: any failure logs and returns a nil client (plus a
// no-op closer), so a missing/unmigrated queue never blocks boot — invitation
// emails simply aren't enqueued (the link is still returned) and Tekton enable
// reports the worker is unavailable.
func buildJobQueue(cfg *config.Config) (*queue.Client, func()) {
	pool, err := queue.Open(context.Background(), cfg.DatabaseURL, 2)
	if err != nil {
		log.Printf("serve: background-job queue unavailable (%v); background jobs will not be enqueued", err)
		return nil, func() {}
	}
	client, err := queue.NewClient(pool, queue.Config{WorkerMode: false, Logger: slog.Default()})
	if err != nil {
		log.Printf("serve: background-job queue client error (%v); background jobs will not be enqueued", err)
		pool.Close()
		return nil, func() {}
	}
	return client, pool.Close
}

// buildHandler composes the full HTTP handler tree given pre-built deps.
// The services may be nil — the API surface returns a clear "not configured"
// error rather than panicking, which keeps route-level tests usable
// without a real database. verifier must be non-nil in production (built from
// the bundled-Dex OIDC config); a nil verifier makes RequireAuth fail closed.
func buildHandler(cfg *config.Config, deps api.ServerDeps, verifier auth.TokenVerifier) http.Handler {
	mux := http.NewServeMux()
	registerRoutes(mux, cfg, deps, verifier)
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
