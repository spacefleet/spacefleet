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
// our case). It validates each incoming bearer as an ID token — signature
// (against the provider's JWKS), issuer, expiry, and audience (aud == clientID)
// — and sets Session.UserID to the token subject (sub).
//
// Signing keys are sourced one of two ways:
//
//   - jwksURL == "": standard OIDC discovery against issuer at startup learns
//     the JWKS endpoint. Discovery needs the provider reachable at construction
//     time; because Dex may still be warming up when `make dev` starts the
//     backend, discovery is retried a few times before giving up.
//   - jwksURL != "": skip discovery and fetch keys directly from jwksURL. This
//     lets the backend verify tokens via an in-cluster address for Dex (the
//     bundled-Dex Service) while still validating the public issuer carried in
//     the token's `iss` claim — so token verification never depends on the
//     public issuer URL being reachable from inside the cluster. The key set is
//     fetched lazily, so there is no startup network call on this path.
func NewOIDCVerifier(ctx context.Context, issuer, clientID, jwksURL string) (TokenVerifier, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	if issuer == "" {
		return nil, errors.New("oidc: empty issuer")
	}
	if clientID == "" {
		return nil, errors.New("oidc: empty client id")
	}

	var verifier *oidc.IDTokenVerifier
	if jwksURL = strings.TrimSpace(jwksURL); jwksURL != "" {
		// context.Background, not ctx: the key set runs background refresh for
		// the life of the process, and ctx is the bounded construction context
		// (cancelled by the caller once we return). This mirrors how go-oidc's
		// own NewProvider detaches the key set from the discovery context.
		keySet := oidc.NewRemoteKeySet(context.Background(), jwksURL)
		verifier = oidc.NewVerifier(issuer, keySet, &oidc.Config{ClientID: clientID})
	} else {
		provider, err := discoverWithRetry(ctx, issuer, 5, time.Second)
		if err != nil {
			return nil, fmt.Errorf("oidc discovery for %q: %w", issuer, err)
		}
		verifier = provider.Verifier(&oidc.Config{ClientID: clientID})
	}

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
		return &Session{UserID: idToken.Subject, Email: claims.Email, ExpiresAt: idToken.Expiry}, nil
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
