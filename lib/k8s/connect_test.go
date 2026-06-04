package k8s

import (
	"context"
	"net"
	"testing"

	"k8s.io/client-go/rest"
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

// TestRESTConfigKubeconfigRejectsExec is the regression guard for the RCE via
// an exec credential plugin in a user-supplied kubeconfig: client-go would run
// the named command on the Spacefleet host during the registration probe.
func TestRESTConfigKubeconfigRejectsExec(t *testing.T) {
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
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: /bin/sh
      args: ["-c", "touch /tmp/pwned"]
`)
	if _, err := RESTConfig(context.Background(), Connection{
		Method:      MethodKubeconfig,
		Credentials: kubeconfig,
	}); err == nil {
		t.Fatal("expected kubeconfig with an exec plugin to be rejected")
	}
}

// TestRESTConfigKubeconfigRejectsAuthProvider guards the legacy auth-provider
// block, rejected for the same reason as exec.
func TestRESTConfigKubeconfigRejectsAuthProvider(t *testing.T) {
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
    auth-provider:
      name: gcp
`)
	if _, err := RESTConfig(context.Background(), Connection{
		Method:      MethodKubeconfig,
		Credentials: kubeconfig,
	}); err == nil {
		t.Fatal("expected kubeconfig with an auth-provider to be rejected")
	}
}

// TestRESTConfigTokenRejectedAtBuild covers the endpoints rejected synchronously
// at config-build time: a non-https scheme, and the unconditionally-denied
// literal addresses (cloud-metadata/link-local, unspecified, multicast) that are
// never a legitimate API server.
func TestRESTConfigTokenRejectedAtBuild(t *testing.T) {
	cases := map[string]string{
		"plain http":          "http://api.example.com:6443",
		"link-local metadata": "https://169.254.169.254",
		"unspecified":         "https://0.0.0.0:6443",
		"multicast":           "https://224.0.0.1:6443",
	}
	for name, endpoint := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := RESTConfig(context.Background(), Connection{
				Method:      MethodToken,
				Endpoint:    endpoint,
				Config:      map[string]string{ConfigKeyInsecureSkipTLS: "true"},
				Credentials: []byte("tok"),
			})
			if err == nil {
				t.Fatalf("expected endpoint %q to be rejected at build", endpoint)
			}
		})
	}
}

// TestRESTConfigTokenLoopbackPrivateBlockedAtDial confirms loopback/private
// literal endpoints build a config (so the same-cluster rewrite path is not
// falsely rejected) but their installed Dial guard refuses the connection — the
// SSRF is blocked at the point it would actually reach the internal network.
func TestRESTConfigTokenLoopbackPrivateBlockedAtDial(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:6443", "10.0.0.5:6443", "192.168.1.10:6443"} {
		cfg, err := RESTConfig(context.Background(), Connection{
			Method:      MethodToken,
			Endpoint:    "https://" + addr,
			Config:      map[string]string{ConfigKeyInsecureSkipTLS: "true"},
			Credentials: []byte("tok"),
		})
		if err != nil {
			t.Fatalf("RESTConfig(%s): unexpected build error: %v", addr, err)
		}
		if cfg.Dial == nil {
			t.Fatalf("RESTConfig(%s): expected a guarded Dial", addr)
		}
		if _, err := cfg.Dial(context.Background(), "tcp", addr); err == nil {
			t.Fatalf("dial to %s should be rejected by the guard", addr)
		}
	}
}

// TestRESTConfigTokenAllowPrivate confirms the operator opt-in flips the
// dial-time guard: a loopback connection that the default policy refuses
// succeeds once AllowPrivate is set — while cloud-metadata / link-local stays
// blocked regardless, since it is never a legitimate API server.
func TestRESTConfigTokenAllowPrivate(t *testing.T) {
	defer SetEndpointPolicy(EndpointPolicy{}) // restore secure default

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	build := func() *rest.Config {
		cfg, err := RESTConfig(context.Background(), Connection{
			Method:      MethodToken,
			Endpoint:    "https://" + addr,
			Config:      map[string]string{ConfigKeyInsecureSkipTLS: "true"},
			Credentials: []byte("tok"),
		})
		if err != nil {
			t.Fatalf("RESTConfig: %v", err)
		}
		return cfg
	}

	// Default policy: the guard refuses to dial the loopback listener.
	if _, err := build().Dial(context.Background(), "tcp", addr); err == nil {
		t.Fatal("default policy should refuse a loopback dial")
	}

	// Opt-in: the same dial now succeeds.
	SetEndpointPolicy(EndpointPolicy{AllowPrivate: true})
	conn, err := build().Dial(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("AllowPrivate should permit the loopback dial: %v", err)
	}
	_ = conn.Close()

	// Metadata/link-local is never a real API server — rejected at build even
	// with AllowPrivate.
	if _, err := RESTConfig(context.Background(), Connection{
		Method:      MethodToken,
		Endpoint:    "https://169.254.169.254",
		Config:      map[string]string{ConfigKeyInsecureSkipTLS: "true"},
		Credentials: []byte("tok"),
	}); err == nil {
		t.Fatal("link-local/metadata must stay blocked even with AllowPrivate=true")
	}
}

// TestRESTConfigKubeconfigSSRFRejected confirms the kubeconfig server URL is
// subject to the same policy as the token endpoint (it is equally
// caller-controlled).
func TestRESTConfigKubeconfigSSRFRejected(t *testing.T) {
	kubeconfig := []byte(`apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://169.254.169.254
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
	if _, err := RESTConfig(context.Background(), Connection{
		Method:      MethodKubeconfig,
		Credentials: kubeconfig,
	}); err == nil {
		t.Fatal("expected kubeconfig pointing at the metadata endpoint to be rejected")
	}
}

// TestRESTConfigTokenSetsGuardedDial confirms a config built for an allowed
// (public) endpoint still carries the dial-time guard, which re-checks the
// resolved IP on every connection and so closes the DNS-rebinding hole.
func TestRESTConfigTokenSetsGuardedDial(t *testing.T) {
	cfg, err := RESTConfig(context.Background(), Connection{
		Method:      MethodToken,
		Endpoint:    "https://api.example.com:6443",
		Config:      map[string]string{ConfigKeyInsecureSkipTLS: "true"},
		Credentials: []byte("tok"),
	})
	if err != nil {
		t.Fatalf("RESTConfig: %v", err)
	}
	if cfg.Dial == nil {
		t.Fatal("expected a guarded Dial to be installed on the config")
	}
}

// TestEndpointPolicyAllowsIP unit-tests the address classifier the up-front
// check and the dial-time Control hook share.
func TestEndpointPolicyAllowsIP(t *testing.T) {
	deny := EndpointPolicy{}
	allowPriv := EndpointPolicy{AllowPrivate: true}

	rejectedByDefault := []string{"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.0.1", "169.254.169.254", "fe80::1", "0.0.0.0"}
	for _, s := range rejectedByDefault {
		if err := deny.allowsIP(net.ParseIP(s)); err == nil {
			t.Errorf("default policy should reject %s", s)
		}
	}
	// AllowPrivate frees up loopback/private, but never link-local/metadata.
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "192.168.0.1"} {
		if err := allowPriv.allowsIP(net.ParseIP(s)); err != nil {
			t.Errorf("AllowPrivate should permit %s: %v", s, err)
		}
	}
	for _, s := range []string{"169.254.169.254", "fe80::1"} {
		if err := allowPriv.allowsIP(net.ParseIP(s)); err == nil {
			t.Errorf("link-local/metadata %s must stay blocked under AllowPrivate", s)
		}
	}
	// A public address is allowed under either policy.
	if err := deny.allowsIP(net.ParseIP("93.184.216.34")); err != nil {
		t.Errorf("public address should be allowed: %v", err)
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
