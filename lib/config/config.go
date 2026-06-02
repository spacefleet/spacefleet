package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// LoginMethod is one selectable sign-in option shown on the login screen. It
// mirrors a Dex connector (id/name) plus its type, so the SPA can render a
// button per method and deep-link straight to that connector
// (?connector_id=<ID>) instead of landing on Dex's own connector-picker.
// Non-secret by design — only the id, display name, and type are exposed.
type LoginMethod struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type Config struct {
	Addr        string
	Env         string
	DatabaseURL string

	// ExternalURL is the canonical public base URL of this deployment, e.g.
	// "https://spacefleet.example.com" (no trailing slash). It is the single
	// source of truth for every user-visible external link the backend builds —
	// notably invitation links — so those URLs never depend on the operator's
	// ingress host configuration. Required: Load() fails when EXTERNAL_URL is
	// unset, the same fail-closed posture as OIDC_ISSUER. Non-secret.
	ExternalURL string

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

	// LoginMethods is the list of sign-in options the login screen offers, read
	// from LOGIN_METHODS (a JSON array of {id,name,type}). It mirrors the Dex
	// connectors the operator configured — Dex has no runtime API to enumerate
	// them, so the operator declares them here (the Helm chart derives this from
	// the same dex.connectors it renders into Dex's config). Empty when unset:
	// the SPA then falls back to a single generic "Sign in" button. Non-secret;
	// surfaced to the browser via /config.js.
	LoginMethods []LoginMethod

	// AllowOrgCreation controls whether users may create new organizations.
	// On by default; set ALLOW_ORG_CREATION=false to lock it down so that
	// only invited users (added to an existing org) can use the app — a
	// user with no memberships is then told to request an invite rather than
	// shown a create-organization screen. Non-secret; surfaced to the browser
	// via /config.js so the SPA can render the right onboarding state, and
	// enforced server-side on the create endpoint regardless.
	AllowOrgCreation bool

	// SMTP settings for outbound email (today: invitation emails). When SMTPHost
	// or SMTPFrom is empty, email is disabled (EmailEnabled reports false): the
	// app still works and invitations still return a copy-able link, an admin
	// just sends it manually. SMTPPassword is a secret and is never surfaced to
	// the browser; whether email is enabled is non-secret and is exposed via
	// /config.js so the SPA can tune its wording.
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
	// SMTPStartTLS upgrades the connection with STARTTLS after connecting (the
	// common submission setup on port 587). Default true.
	SMTPStartTLS bool
}

// EmailEnabled reports whether outbound email is configured. Invitations always
// return a copy-able link; an email is only sent in addition when this is true.
func (c *Config) EmailEnabled() bool {
	return c.SMTPHost != "" && c.SMTPFrom != ""
}

func Load() (*Config, error) {
	cfg := &Config{
		Addr:           getenv("ADDR", ":8080"),
		Env:            getenv("ENV", "development"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		ExternalURL:    strings.TrimRight(os.Getenv("EXTERNAL_URL"), "/"),
		OIDCIssuer:     os.Getenv("OIDC_ISSUER"),
		OIDCClientID:   os.Getenv("OIDC_CLIENT_ID"),
		OIDCJWKSURL:    os.Getenv("OIDC_JWKS_URL"),
		DexUpstreamURL: os.Getenv("DEX_UPSTREAM_URL"),
		SecretKey:      os.Getenv("SPACEFLEET_SECRET_KEY"),
		SMTPHost:       os.Getenv("SMTP_HOST"),
		SMTPUsername:   os.Getenv("SMTP_USERNAME"),
		SMTPPassword:   os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:       os.Getenv("SMTP_FROM"),
	}

	// EXTERNAL_URL is mandatory — external links (e.g. invitations) must not
	// silently fall back to a guessed origin. Fail closed, like OIDC_ISSUER.
	if cfg.ExternalURL == "" {
		return nil, fmt.Errorf("EXTERNAL_URL is required: it is the canonical public base URL used to build external links (e.g. https://spacefleet.example.com)")
	}

	concurrency, err := parsePositiveInt("WORKER_CONCURRENCY", 4)
	if err != nil {
		return nil, err
	}
	cfg.WorkerConcurrency = concurrency

	allowOrgCreation, err := parseBool("ALLOW_ORG_CREATION", true)
	if err != nil {
		return nil, err
	}
	cfg.AllowOrgCreation = allowOrgCreation

	smtpPort, err := parsePositiveInt("SMTP_PORT", 587)
	if err != nil {
		return nil, err
	}
	cfg.SMTPPort = smtpPort

	smtpStartTLS, err := parseBool("SMTP_STARTTLS", true)
	if err != nil {
		return nil, err
	}
	cfg.SMTPStartTLS = smtpStartTLS

	loginMethods, err := parseLoginMethods("LOGIN_METHODS")
	if err != nil {
		return nil, err
	}
	cfg.LoginMethods = loginMethods

	return cfg, nil
}

// parseLoginMethods reads a JSON array of login methods from the named env var,
// e.g. LOGIN_METHODS=[{"id":"github","name":"GitHub","type":"github"}]. Unset
// returns an empty (non-nil) slice so /config.js emits [] rather than null and
// the SPA shows its generic "Sign in" fallback. Malformed JSON is rejected
// rather than silently dropped — a typo shouldn't quietly hide every login
// button.
func parseLoginMethods(key string) ([]LoginMethod, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return []LoginMethod{}, nil
	}
	var methods []LoginMethod
	if err := json.Unmarshal([]byte(raw), &methods); err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return methods, nil
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

// parseBool reads a boolean env var (strconv.ParseBool syntax: 1/0, t/f,
// true/false), falling back to fallback when unset. A malformed value is
// rejected rather than silently treated as false, so a typo can't quietly flip
// a security setting.
func parseBool(key string, fallback bool) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return v, nil
}
