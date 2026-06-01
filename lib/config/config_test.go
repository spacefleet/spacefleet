package config

import "testing"

// TestLoadDefaults verifies the values Load picks when no env is set.
func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

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
	if cfg.WorkerConcurrency != 4 {
		t.Errorf("WorkerConcurrency = %d, want 4", cfg.WorkerConcurrency)
	}
	if !cfg.AllowOrgCreation {
		t.Errorf("AllowOrgCreation = %v, want true (on by default)", cfg.AllowOrgCreation)
	}
}

func TestLoadOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("ADDR", ":9090")
	t.Setenv("DATABASE_URL", "postgres://localhost/app")
	t.Setenv("OIDC_ISSUER", "https://dex.example.com")
	t.Setenv("OIDC_CLIENT_ID", "spacefleet")
	t.Setenv("WORKER_CONCURRENCY", "8")
	t.Setenv("ALLOW_ORG_CREATION", "false")

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
}

func TestLoadRejectsBadConcurrency(t *testing.T) {
	for _, v := range []string{"0", "-1", "abc"} {
		t.Run(v, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("WORKER_CONCURRENCY", v)
			if _, err := Load(); err == nil {
				t.Errorf("expected error for concurrency=%q", v)
			}
		})
	}
}

func TestLoadRejectsBadAllowOrgCreation(t *testing.T) {
	clearEnv(t)
	t.Setenv("ALLOW_ORG_CREATION", "maybe")
	if _, err := Load(); err == nil {
		t.Error("expected error for ALLOW_ORG_CREATION=maybe")
	}
}

// clearEnv unsets every key Load reads so tests start from a known
// baseline.
func clearEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"ADDR",
		"ENV",
		"DATABASE_URL",
		"OIDC_ISSUER",
		"OIDC_CLIENT_ID",
		"WORKER_CONCURRENCY",
		"ALLOW_ORG_CREATION",
	}
	for _, k := range keys {
		t.Setenv(k, "")
	}
}
