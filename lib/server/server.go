package server

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"time"

	"github.com/spacefleet/spacefleet/lib/api"
	"github.com/spacefleet/spacefleet/lib/applications"
	"github.com/spacefleet/spacefleet/lib/auth"
	"github.com/spacefleet/spacefleet/lib/chartcredentials"
	"github.com/spacefleet/spacefleet/lib/cloudcredentials"
	"github.com/spacefleet/spacefleet/lib/clusters"
	"github.com/spacefleet/spacefleet/lib/config"
	"github.com/spacefleet/spacefleet/lib/db"
	"github.com/spacefleet/spacefleet/lib/githubapp"
	"github.com/spacefleet/spacefleet/lib/githubinstallations"
	"github.com/spacefleet/spacefleet/lib/invitations"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/organizations"
	"github.com/spacefleet/spacefleet/lib/queue"
	"github.com/spacefleet/spacefleet/lib/secrets"
	"github.com/spacefleet/spacefleet/lib/users"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// New wires runtime dependencies (Postgres via ent) and returns a
// ready-to-serve *http.Server. Closing those dependencies is registered
// with Server.RegisterOnShutdown so callers only drive the HTTP lifecycle.
func New(cfg *config.Config) (*http.Server, error) {
	// Install the process-wide SSRF policy for user-supplied cluster endpoints
	// before any cluster probe can run.
	k8s.SetEndpointPolicy(k8s.EndpointPolicy{AllowPrivate: cfg.AllowPrivateClusterEndpoints})

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

	// GitHub App authenticator for pulling charts from private Git repositories.
	// Nil (feature off) when no App is configured; a configured-but-unparseable
	// key fails boot, since the operator clearly intended the feature.
	ghAuth, err := buildGitHubAuthenticator(cfg)
	if err != nil {
		_ = entClient.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("build github app: %w", err)
	}

	usersSvc := users.NewService(entClient)
	orgsSvc := organizations.NewService(entClient)
	clustersSvc := clusters.NewService(entClient, sealer)
	chartCredsSvc := chartcredentials.NewService(entClient, sealer)
	cloudCredsSvc := cloudcredentials.NewService(entClient, sealer)
	githubInstallsSvc := githubinstallations.NewService(entClient, ghAuth)
	applicationsSvc := applications.NewService(entClient)
	workflowsSvc := workflows.NewService(entClient)
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
		Users:               usersSvc,
		Orgs:                orgsSvc,
		Clusters:            clustersSvc,
		Applications:        applicationsSvc,
		ChartCredentials:    chartCredsSvc,
		CloudCredentials:    cloudCredsSvc,
		GitHubInstallations: githubInstallsSvc,
		Invites:             invitesSvc,
		Workflows:           workflowsSvc,
		AllowOrgCreation:    cfg.AllowOrgCreation,
		ExternalURL:         cfg.ExternalURL,
		EmailEnabled:        cfg.EmailEnabled(),
		GitHubAppSlug:       cfg.GitHubAppSlug,
		SecretKey:           cfg.SecretKey,
		JobQueue:            jobQueue,
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

// buildGitHubAuthenticator builds the GitHub App authenticator for pulling
// charts from private Git repositories, or returns a nil authenticator (typed
// as the installations service's interface) when no App is configured — the
// feature is then simply off. A configured-but-unparseable key is a real
// misconfiguration of an explicitly-enabled feature, so it errors (boot fails).
func buildGitHubAuthenticator(cfg *config.Config) (githubinstallations.Authenticator, error) {
	if !cfg.GitHubAppEnabled() {
		return nil, nil
	}
	auth, err := githubapp.New(cfg.GitHubAppID, cfg.GitHubAppPrivateKey)
	if err != nil {
		return nil, err
	}
	return auth, nil
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
