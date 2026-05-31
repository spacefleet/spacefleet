package k8s

import (
	"context"
	"testing"
)

func TestRESTConfigToken(t *testing.T) {
	conn := Connection{
		Method:      MethodToken,
		Endpoint:    "https://api.example.com:6443",
		Config:      map[string]string{ConfigKeyCA: "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"},
		Credentials: []byte("sa-bearer-token"),
	}
	cfg, err := RESTConfig(context.Background(), conn)
	if err != nil {
		t.Fatalf("RESTConfig: %v", err)
	}
	if cfg.Host != conn.Endpoint {
		t.Errorf("Host = %q, want %q", cfg.Host, conn.Endpoint)
	}
	if cfg.BearerToken != "sa-bearer-token" {
		t.Errorf("BearerToken = %q", cfg.BearerToken)
	}
	if string(cfg.CAData) == "" {
		t.Error("expected CAData to be set")
	}
	if cfg.Insecure {
		t.Error("expected TLS verification enabled when CA provided")
	}
}

func TestRESTConfigTokenInsecure(t *testing.T) {
	conn := Connection{
		Method:      MethodToken,
		Endpoint:    "https://api.example.com:6443",
		Config:      map[string]string{ConfigKeyInsecureSkipTLS: "true"},
		Credentials: []byte("tok"),
	}
	cfg, err := RESTConfig(context.Background(), conn)
	if err != nil {
		t.Fatalf("RESTConfig: %v", err)
	}
	if !cfg.Insecure {
		t.Error("expected Insecure TLS when insecure_skip_tls=true")
	}
}

func TestRESTConfigTokenValidation(t *testing.T) {
	cases := map[string]Connection{
		"missing endpoint": {Method: MethodToken, Credentials: []byte("tok")},
		"missing token":    {Method: MethodToken, Endpoint: "https://x"},
	}
	for name, conn := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := RESTConfig(context.Background(), conn); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestRESTConfigKubeconfig(t *testing.T) {
	kubeconfig := []byte(`apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://api.example.com:6443
    insecure-skip-tls-verify: true
contexts:
- name: c
  context:
    cluster: c
    user: u
current-context: c
users:
- name: u
  user:
    token: abc123
`)
	cfg, err := RESTConfig(context.Background(), Connection{
		Method:      MethodKubeconfig,
		Credentials: kubeconfig,
	})
	if err != nil {
		t.Fatalf("RESTConfig: %v", err)
	}
	if cfg.Host != "https://api.example.com:6443" {
		t.Errorf("Host = %q", cfg.Host)
	}
	if cfg.BearerToken != "abc123" {
		t.Errorf("BearerToken = %q", cfg.BearerToken)
	}
}

func TestRESTConfigUnknownMethod(t *testing.T) {
	if _, err := RESTConfig(context.Background(), Connection{Method: "bogus"}); err == nil {
		t.Fatal("expected error for unknown method")
	}
}

// TestRESTConfigCloudValidation confirms the cloud methods reject missing
// required fields up front (before any network call), so a misconfigured
// registration fails fast with a clear error rather than a deep SDK failure.
func TestRESTConfigCloudValidation(t *testing.T) {
	for _, m := range []Method{MethodEKS, MethodGKE, MethodAKS} {
		if _, err := RESTConfig(context.Background(), Connection{Method: m}); err == nil {
			t.Errorf("method %q: expected validation error for empty connection", m)
		}
	}
}
