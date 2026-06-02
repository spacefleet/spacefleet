package config

import "testing"

// TestLoadDefaults verifies the values Load picks when only the required
// EXTERNAL_URL is set.
func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("EXTERNAL_URL", "https://app.example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", cfg.Addr)
	}
	if cfg.Env != "development" {
		t.Errorf("Env = %q, want development", cfg.Env)
	}
	if cfg.ExternalURL != "https://app.example.com" {
		t.Errorf("ExternalURL = %q", cfg.ExternalURL)
	}
	if cfg.WorkerConcurrency != 4 {
		t.Errorf("WorkerConcurrency = %d, want 4", cfg.WorkerConcurrency)
	}
	if !cfg.AllowOrgCreation {
		t.Errorf("AllowOrgCreation = %v, want true (on by default)", cfg.AllowOrgCreation)
	}
	if cfg.SMTPPort != 587 {
		t.Errorf("SMTPPort = %d, want 587", cfg.SMTPPort)
	}
	if !cfg.SMTPStartTLS {
		t.Errorf("SMTPStartTLS = %v, want true", cfg.SMTPStartTLS)
	}
	if cfg.EmailEnabled() {
		t.Errorf("EmailEnabled = true, want false (no SMTP host/from)")
	}
}

// TestLoadRequiresExternalURL confirms Load fails closed without EXTERNAL_URL.
func TestLoadRequiresExternalURL(t *testing.T) {
	clearEnv(t)
	if _, err := Load(); err == nil {
		t.Fatal("expected Load to fail when EXTERNAL_URL is unset")
	}
}

func TestLoadOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("ADDR", ":9090")
	t.Setenv("DATABASE_URL", "postgres://localhost/app")
	// Trailing slash should be trimmed.
	t.Setenv("EXTERNAL_URL", "https://app.example.com/")
	t.Setenv("OIDC_ISSUER", "https://dex.example.com")
	t.Setenv("OIDC_CLIENT_ID", "spacefleet")
	t.Setenv("WORKER_CONCURRENCY", "8")
	t.Setenv("ALLOW_ORG_CREATION", "false")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_PORT", "2525")
	t.Setenv("SMTP_FROM", "no-reply@example.com")
	t.Setenv("SMTP_STARTTLS", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":9090" {
		t.Errorf("Addr = %q, want :9090", cfg.Addr)
	}
	if cfg.DatabaseURL != "postgres://localhost/app" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.ExternalURL != "https://app.example.com" {
		t.Errorf("ExternalURL = %q, want trailing slash trimmed", cfg.ExternalURL)
	}
	if cfg.OIDCIssuer != "https://dex.example.com" {
		t.Errorf("OIDCIssuer = %q", cfg.OIDCIssuer)
	}
	if cfg.OIDCClientID != "spacefleet" {
		t.Errorf("OIDCClientID = %q", cfg.OIDCClientID)
	}
	if cfg.WorkerConcurrency != 8 {
		t.Errorf("WorkerConcurrency = %d, want 8", cfg.WorkerConcurrency)
	}
	if cfg.AllowOrgCreation {
		t.Errorf("AllowOrgCreation = %v, want false", cfg.AllowOrgCreation)
	}
	if cfg.SMTPPort != 2525 || cfg.SMTPStartTLS {
		t.Errorf("SMTP port/startTLS = %d/%v, want 2525/false", cfg.SMTPPort, cfg.SMTPStartTLS)
	}
	if !cfg.EmailEnabled() {
		t.Errorf("EmailEnabled = false, want true (host+from set)")
	}
}

func TestLoadRejectsBadConcurrency(t *testing.T) {
	for _, v := range []string{"0", "-1", "abc"} {
		t.Run(v, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("EXTERNAL_URL", "https://app.example.com")
			t.Setenv("WORKER_CONCURRENCY", v)
			if _, err := Load(); err == nil {
				t.Errorf("expected error for concurrency=%q", v)
			}
		})
	}
}

func TestLoadRejectsBadAllowOrgCreation(t *testing.T) {
	clearEnv(t)
	t.Setenv("EXTERNAL_URL", "https://app.example.com")
	t.Setenv("ALLOW_ORG_CREATION", "maybe")
	if _, err := Load(); err == nil {
		t.Error("expected error for ALLOW_ORG_CREATION=maybe")
	}
}

// TestLoadLoginMethods covers the three LOGIN_METHODS states: unset (empty,
// non-nil slice so /config.js emits []), a valid JSON array, and malformed JSON
// (rejected — a typo must not silently hide every login button).
func TestLoadLoginMethods(t *testing.T) {
	t.Run("unset is empty", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("EXTERNAL_URL", "https://app.example.com")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.LoginMethods == nil || len(cfg.LoginMethods) != 0 {
			t.Errorf("LoginMethods = %#v, want empty non-nil slice", cfg.LoginMethods)
		}
	})

	t.Run("valid JSON", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("EXTERNAL_URL", "https://app.example.com")
		t.Setenv("LOGIN_METHODS", `[{"id":"github","name":"GitHub","type":"github"},{"id":"local","name":"Email and password","type":"password"}]`)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(cfg.LoginMethods) != 2 {
			t.Fatalf("LoginMethods len = %d, want 2", len(cfg.LoginMethods))
		}
		if cfg.LoginMethods[0] != (LoginMethod{ID: "github", Name: "GitHub", Type: "github"}) {
			t.Errorf("LoginMethods[0] = %#v", cfg.LoginMethods[0])
		}
	})

	t.Run("malformed JSON is rejected", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("EXTERNAL_URL", "https://app.example.com")
		t.Setenv("LOGIN_METHODS", `not json`)
		if _, err := Load(); err == nil {
			t.Error("expected error for malformed LOGIN_METHODS")
		}
	})
}

// clearEnv unsets every key Load reads so tests start from a known
// baseline.
func clearEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"ADDR",
		"ENV",
		"DATABASE_URL",
		"EXTERNAL_URL",
		"OIDC_ISSUER",
		"OIDC_CLIENT_ID",
		"WORKER_CONCURRENCY",
		"ALLOW_ORG_CREATION",
		"LOGIN_METHODS",
		"SMTP_HOST",
		"SMTP_PORT",
		"SMTP_USERNAME",
		"SMTP_PASSWORD",
		"SMTP_FROM",
		"SMTP_STARTTLS",
	}
	for _, k := range keys {
		t.Setenv(k, "")
	}
}
