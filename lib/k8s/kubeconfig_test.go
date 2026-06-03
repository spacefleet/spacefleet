package k8s

import (
	"context"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

// TestKubeconfigTokenRoundTrip: a token-method connection serializes to a
// kubeconfig whose host/CA/token survive a parse round-trip.
func TestKubeconfigTokenRoundTrip(t *testing.T) {
	conn := Connection{
		Method:      MethodToken,
		Endpoint:    "https://api.example.com:6443",
		Config:      map[string]string{ConfigKeyCA: "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"},
		Credentials: []byte("sa-bearer-token"),
	}
	kc, err := Kubeconfig(context.Background(), conn)
	if err != nil {
		t.Fatalf("Kubeconfig: %v", err)
	}
	raw, err := clientcmd.Load(kc)
	if err != nil {
		t.Fatalf("parse kubeconfig: %v", err)
	}
	kctx := raw.Contexts[raw.CurrentContext]
	if kctx == nil {
		t.Fatal("no current context")
	}
	cl := raw.Clusters[kctx.Cluster]
	if cl.Server != conn.Endpoint {
		t.Errorf("server = %q, want %q", cl.Server, conn.Endpoint)
	}
	if string(cl.CertificateAuthorityData) == "" {
		t.Error("expected CA data")
	}
	if cl.InsecureSkipTLSVerify {
		t.Error("expected TLS verification enabled")
	}
	if ai := raw.AuthInfos[kctx.AuthInfo]; ai.Token != "sa-bearer-token" {
		t.Errorf("token = %q", ai.Token)
	}
}

// TestKubeconfigInsecure: insecure_skip_tls produces an insecure cluster entry
// with no CA.
func TestKubeconfigInsecure(t *testing.T) {
	conn := Connection{
		Method:      MethodToken,
		Endpoint:    "https://api.example.com:6443",
		Config:      map[string]string{ConfigKeyInsecureSkipTLS: "true"},
		Credentials: []byte("tok"),
	}
	kc, err := Kubeconfig(context.Background(), conn)
	if err != nil {
		t.Fatalf("Kubeconfig: %v", err)
	}
	raw, err := clientcmd.Load(kc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cl := raw.Clusters[raw.Contexts[raw.CurrentContext].Cluster]
	if !cl.InsecureSkipTLSVerify {
		t.Error("expected InsecureSkipTLSVerify")
	}
	if len(cl.CertificateAuthorityData) != 0 {
		t.Error("expected no CA when insecure")
	}
}

// TestKubeconfigClientCertRoundTrip: a kubeconfig-method connection carrying a
// client cert/key (no token) round-trips the cert material.
func TestKubeconfigClientCertRoundTrip(t *testing.T) {
	// A minimal kubeconfig with embedded client cert/key (base64 of dummy PEM).
	// dGVzdA== is base64("test"); clientcmd accepts arbitrary bytes here.
	src := []byte(`apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://api.example.com:6443
    certificate-authority-data: dGVzdA==
users:
- name: u
  user:
    client-certificate-data: dGVzdA==
    client-key-data: dGVzdA==
contexts:
- name: ctx
  context:
    cluster: c
    user: u
current-context: ctx
`)
	conn := Connection{Method: MethodKubeconfig, Credentials: src}
	kc, err := Kubeconfig(context.Background(), conn)
	if err != nil {
		t.Fatalf("Kubeconfig: %v", err)
	}
	raw, err := clientcmd.Load(kc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ai := raw.AuthInfos[raw.Contexts[raw.CurrentContext].AuthInfo]
	if len(ai.ClientCertificateData) == 0 || len(ai.ClientKeyData) == 0 {
		t.Errorf("expected client cert + key to round-trip, got cert=%d key=%d", len(ai.ClientCertificateData), len(ai.ClientKeyData))
	}
	if ai.Token != "" {
		t.Errorf("did not expect a token, got %q", ai.Token)
	}
}
