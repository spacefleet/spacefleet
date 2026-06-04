package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// testKeyPEM generates a throwaway RSA key as PEM for constructing an
// Authenticator in tests.
func testKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
}

func TestNewRejectsBadKey(t *testing.T) {
	if _, err := New(123, "not a pem"); err == nil {
		t.Fatal("expected an error for an unparseable key")
	}
	if _, err := New(0, testKeyPEM(t)); err == nil {
		t.Fatal("expected an error for a zero app id")
	}
}

func TestAppJWT(t *testing.T) {
	a, err := New(456, testKeyPEM(t))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	tok, err := a.AppJWT()
	if err != nil {
		t.Fatalf("app jwt: %v", err)
	}
	// Parse without verifying (we only assert the claims shape) — the issuer is
	// the app id and there is an unexpired exp.
	parsed, _, err := jwt.NewParser().ParseUnverified(tok, jwt.MapClaims{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if claims["iss"] != "456" {
		t.Errorf("iss = %v, want 456", claims["iss"])
	}
	if _, ok := claims["exp"]; !ok {
		t.Error("missing exp claim")
	}
}

func TestInstallationTokenAndGetInstallation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Errorf("missing Authorization header on %s", r.URL.Path)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/99/access_tokens":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"ghs_minted","expires_at":"2026-06-03T12:00:00Z"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/app/installations/99":
			_, _ = w.Write([]byte(`{"account":{"login":"acme","type":"Organization"}}`))
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	a, err := New(1, testKeyPEM(t))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	a.baseURL = srv.URL

	tok, _, err := a.InstallationToken(context.Background(), 99)
	if err != nil {
		t.Fatalf("installation token: %v", err)
	}
	if tok != "ghs_minted" {
		t.Errorf("token = %q, want ghs_minted", tok)
	}

	inst, err := a.GetInstallation(context.Background(), 99)
	if err != nil {
		t.Fatalf("get installation: %v", err)
	}
	if inst.Login != "acme" || inst.AccountType != "Organization" {
		t.Errorf("installation = %+v, want acme/Organization", inst)
	}
}

func TestInstallationTokenPropagatesErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no access", http.StatusNotFound)
	}))
	defer srv.Close()
	a, _ := New(1, testKeyPEM(t))
	a.baseURL = srv.URL
	if _, _, err := a.InstallationToken(context.Background(), 7); err == nil {
		t.Fatal("expected an error from a non-2xx response")
	}
}

// testSecretKey is a base64-encoded 32-byte key, matching the SPACEFLEET_SECRET_KEY format.
func testSecretKey() string {
	return base64.StdEncoding.EncodeToString(make([]byte, 32))
}

func TestSignVerifyStateRoundTrip(t *testing.T) {
	key := testSecretKey()
	org := uuid.New()
	state, err := SignState(key, org)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	got, err := VerifyState(key, state)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got != org {
		t.Errorf("verified org = %v, want %v", got, org)
	}
}

func TestVerifyStateRejectsTampered(t *testing.T) {
	key := testSecretKey()
	state, err := SignState(key, uuid.New())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Flip the payload (before the dot) so the MAC no longer matches.
	body, mac, _ := strings.Cut(state, ".")
	tampered := body + "x." + mac
	if _, err := VerifyState(key, tampered); err == nil {
		t.Fatal("expected a signature mismatch for a tampered state")
	}
	if _, err := VerifyState(key, "garbage"); err == nil {
		t.Fatal("expected an error for a malformed state")
	}
}

func TestStateRequiresSecretKey(t *testing.T) {
	if _, err := SignState("", uuid.New()); err == nil {
		t.Fatal("expected an error signing with an empty key")
	}
}
