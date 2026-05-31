package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// oidcTestEnv is a fake OIDC signing setup: an httptest server publishing a
// JWKS, plus the private key to mint ID tokens against it. It exists so the
// jwksURL path of NewOIDCVerifier can be exercised end to end without a real
// Dex — the backend fetches keys from jwksURL and validates the issuer carried
// in the token, exactly the bundled-Dex flow.
type oidcTestEnv struct {
	jwksURL string
	priv    *rsa.PrivateKey
	kid     string
}

func newOIDCTestEnv(t *testing.T) *oidcTestEnv {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	const kid = "test-key"
	jwks, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &priv.PublicKey, KeyID: kid, Algorithm: "RS256", Use: "sig"},
	}})
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwks)
	}))
	t.Cleanup(srv.Close)
	return &oidcTestEnv{jwksURL: srv.URL + "/keys", priv: priv, kid: kid}
}

// sign mints a signed compact JWT (an ID token) from the given claims.
func (e *oidcTestEnv) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: e.priv, KeyID: e.kid, Algorithm: "RS256"}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	obj, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	token, err := obj.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return token
}

func idClaims(issuer, aud string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":   issuer,
		"sub":   "user-123",
		"aud":   aud,
		"email": "user@example.com",
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
}

const (
	testIssuer   = "https://auth.example.com/dex"
	testClientID = "spacefleet"
)

func TestNewOIDCVerifierConstructionValidation(t *testing.T) {
	ctx := context.Background()
	if _, err := NewOIDCVerifier(ctx, "", testClientID, "https://x/keys"); err == nil {
		t.Fatal("expected error for empty issuer")
	}
	if _, err := NewOIDCVerifier(ctx, testIssuer, "", "https://x/keys"); err == nil {
		t.Fatal("expected error for empty client id")
	}
}

func TestOIDCVerifierJWKSURLPath(t *testing.T) {
	env := newOIDCTestEnv(t)
	ctx := context.Background()

	// The jwksURL path must not require network at construction (keys are
	// fetched lazily) even though no discovery endpoint exists for testIssuer.
	verify, err := NewOIDCVerifier(ctx, testIssuer, testClientID, env.jwksURL)
	if err != nil {
		t.Fatalf("NewOIDCVerifier: %v", err)
	}

	t.Run("valid token", func(t *testing.T) {
		sess, err := verify(ctx, env.sign(t, idClaims(testIssuer, testClientID)))
		if err != nil {
			t.Fatalf("verify valid: %v", err)
		}
		if sess.UserID != "user-123" {
			t.Errorf("UserID = %q, want user-123", sess.UserID)
		}
		if sess.Email != "user@example.com" {
			t.Errorf("Email = %q, want user@example.com", sess.Email)
		}
	})

	t.Run("wrong audience", func(t *testing.T) {
		if _, err := verify(ctx, env.sign(t, idClaims(testIssuer, "someone-else"))); err == nil {
			t.Fatal("expected error for aud != clientID")
		}
	})

	t.Run("wrong issuer", func(t *testing.T) {
		if _, err := verify(ctx, env.sign(t, idClaims("https://evil.example.com", testClientID))); err == nil {
			t.Fatal("expected error for mismatched iss")
		}
	})

	t.Run("expired token", func(t *testing.T) {
		c := idClaims(testIssuer, testClientID)
		c["exp"] = time.Now().Add(-time.Hour).Unix()
		if _, err := verify(ctx, env.sign(t, c)); err == nil {
			t.Fatal("expected error for expired token")
		}
	})

	t.Run("empty bearer", func(t *testing.T) {
		if _, err := verify(ctx, ""); err == nil {
			t.Fatal("expected error for empty bearer")
		}
	})
}
