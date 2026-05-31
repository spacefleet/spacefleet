package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// NewOIDCVerifier builds a TokenVerifier backed by an OIDC provider (Dex in
// our case). It performs OIDC discovery against issuer at startup to fetch
// the signing keys (JWKS) endpoint, then validates each incoming bearer as an
// ID token: signature, issuer, expiry, and audience (aud == clientID).
//
// The returned verifier sets Session.UserID to the token subject (sub).
//
// Discovery needs the provider reachable at construction time. Because the
// Dex container may still be warming up when `make dev` starts the backend,
// we retry discovery a few times before giving up.
func NewOIDCVerifier(ctx context.Context, issuer, clientID string) (TokenVerifier, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	if issuer == "" {
		return nil, errors.New("oidc: empty issuer")
	}
	if clientID == "" {
		return nil, errors.New("oidc: empty client id")
	}

	provider, err := discoverWithRetry(ctx, issuer, 5, time.Second)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %q: %w", issuer, err)
	}

	verifier := provider.Verifier(&oidc.Config{ClientID: clientID})

	return func(ctx context.Context, bearer string) (*Session, error) {
		bearer = strings.TrimSpace(bearer)
		if bearer == "" {
			return nil, errors.New("oidc: empty bearer token")
		}
		idToken, err := verifier.Verify(ctx, bearer)
		if err != nil {
			return nil, fmt.Errorf("oidc: verify token: %w", err)
		}
		if idToken.Subject == "" {
			return nil, errors.New("oidc: token has no subject")
		}
		// The email claim is best-effort: it drives the local user record but
		// isn't required for a valid session. Ignore the decode error so a
		// token without an email still authenticates.
		var claims struct {
			Email string `json:"email"`
		}
		_ = idToken.Claims(&claims)
		return &Session{UserID: idToken.Subject, Email: claims.Email}, nil
	}, nil
}

// discoverWithRetry calls oidc.NewProvider up to attempts times, pausing delay
// between tries. It returns as soon as discovery succeeds, or the last error
// (or ctx error) once attempts are exhausted.
func discoverWithRetry(ctx context.Context, issuer string, attempts int, delay time.Duration) (*oidc.Provider, error) {
	var lastErr error
	for range attempts {
		provider, err := oidc.NewProvider(ctx, issuer)
		if err == nil {
			return provider, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, lastErr
}
