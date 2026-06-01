package auth

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
)

// TokenVerifier resolves a bearer token to a Session, or returns an error
// explaining why it's invalid. The Dex (OIDC) verifier (see oidc.go) is the
// only production implementation — Spacefleet always authenticates against
// its bundled Dex. Tests inject a fake (see lib/testsupport).
type TokenVerifier func(ctx context.Context, bearer string) (*Session, error)

// RequireAuth verifies the Authorization header on every request whose path
// isn't in publicPaths, delegating the actual check to verify. On success
// the resolved Session is attached to the request context (FromContext).
//
// verify must be non-nil. A nil verifier is treated as a misconfiguration and
// RequireAuth fails closed — every protected request gets a 401 — rather than
// silently authenticating everyone. (Boot also refuses to start without an
// OIDC issuer; this is the defense in depth behind that.)
//
// Responses: 401 when no credential is provided or verification fails.
func RequireAuth(publicPaths []string, verify TokenVerifier) func(http.Handler) http.Handler {
	public := make(map[string]struct{}, len(publicPaths))
	for _, p := range publicPaths {
		public[p] = struct{}{}
	}
	if verify == nil {
		log.Print("auth: no TokenVerifier configured — failing closed, every protected request will be rejected")
		verify = func(context.Context, string) (*Session, error) {
			return nil, errors.New("auth: no verifier configured")
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := public[r.URL.Path]; ok {
				next.ServeHTTP(w, r)
				return
			}
			// We pass the (possibly empty) bearer token straight to the
			// verifier and let it decide. The dev verifier accepts an empty
			// token; a real OIDC verifier should reject it.
			sess, err := verify(r.Context(), bearerToken(r))
			if err != nil || sess == nil {
				writeJSONError(w, http.StatusUnauthorized, "unauthorized", "invalid token")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithSession(r.Context(), sess)))
		})
	}
}

func bearerToken(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}
