package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Addr        string
	Env         string
	DatabaseURL string

	// OIDC (Dex) auth seam. Issuer is the OIDC issuer URL; ClientID is this
	// app's OIDC client. Both are also surfaced to the browser via /config.js
	// so the SPA can run its own OIDC flow. They are non-secret by design.
	OIDCIssuer   string
	OIDCClientID string

	// OIDCJWKSURL, when set, is the JWKS (signing keys) endpoint the backend
	// fetches keys from to verify ID tokens — instead of learning it via OIDC
	// discovery against OIDCIssuer. This decouples token *verification* (a
	// server-side, in-cluster concern) from the public issuer URL the browser
	// uses: with bundled in-cluster Dex it points at the Dex Service (e.g.
	// http://<release>-dex:5556/dex/keys), so the backend never has to reach
	// the public issuer URL (no ingress "hairpin"). Tokens are still validated
	// against OIDCIssuer in their `iss` claim. Empty = use discovery. This is
	// non-secret. Only the browser uses OIDCIssuer for its OIDC flow.
	OIDCJWKSURL string

	// DexUpstreamURL, when set, is the base URL of the bundled Dex the server
	// reverse-proxies the public /dex/* routes to (browser-facing discovery,
	// auth, token, and keys endpoints). This makes Dex same-origin with the app
	// — the app is the single front door, and Dex is never exposed directly.
	// In dev it points at the docker-compose Dex (http://localhost:5556); in the
	// Helm chart it points at the in-cluster Dex Service. Empty = the proxy is
	// not mounted (e.g. route tests, which don't exercise the login flow). This
	// is non-secret. Dex serves its routes under the issuer path (/dex), so the
	// proxy forwards /dex/* straight through without rewriting.
	DexUpstreamURL string

	// SecretKey is the symmetric key used to envelope-encrypt credentials at
	// rest (e.g. registered cluster tokens/kubeconfigs). It is a base64-encoded
	// 32-byte key, read from SPACEFLEET_SECRET_KEY. When empty, secret sealing
	// is disabled: features that store no secrets (e.g. in-cluster cluster
	// registration) keep working, but registering anything with credentials
	// fails fast with a clear "set SPACEFLEET_SECRET_KEY" error. This is a
	// secret — never surface it to the browser via /config.js.
	SecretKey string

	// WorkerConcurrency caps the number of background jobs the worker
	// process runs in parallel. Default 4.
	WorkerConcurrency int
}

func Load() (*Config, error) {
	cfg := &Config{
		Addr:           getenv("ADDR", ":8080"),
		Env:            getenv("ENV", "development"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		OIDCIssuer:     os.Getenv("OIDC_ISSUER"),
		OIDCClientID:   os.Getenv("OIDC_CLIENT_ID"),
		OIDCJWKSURL:    os.Getenv("OIDC_JWKS_URL"),
		DexUpstreamURL: os.Getenv("DEX_UPSTREAM_URL"),
		SecretKey:      os.Getenv("SPACEFLEET_SECRET_KEY"),
	}

	concurrency, err := parsePositiveInt("WORKER_CONCURRENCY", 4)
	if err != nil {
		return nil, err
	}
	cfg.WorkerConcurrency = concurrency

	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parsePositiveInt reads an integer env var, falling back to fallback when
// unset. Zero or negative values are rejected so a typo doesn't silently
// disable the worker.
func parsePositiveInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if v <= 0 {
		return 0, fmt.Errorf("%s: must be > 0, got %d", key, v)
	}
	return v, nil
}
