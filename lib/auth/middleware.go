package auth

import (
	"context"
	"log"
	"net/http"
	"strings"
)

// TokenVerifier resolves a bearer token to a Session, or returns an error
// explaining why it's invalid. The Dex/OIDC verifier (to be written) is
// the production implementation; NewDevVerifier is the local stand-in.
type TokenVerifier func(ctx context.Context, bearer string) (*Session, error)

// NewDevVerifier returns a verifier that accepts every request and attaches
// a fixed development identity. It exists so the app is runnable before Dex
// is wired up. NEVER use it in production — it performs no verification.
//
// If a bearer token is present it's used verbatim as the user id (handy for
// faking different users in dev); otherwise the session is "dev-user".
func NewDevVerifier() TokenVerifier {
	return func(_ context.Context, bearer string) (*Session, error) {
		uid := strings.TrimSpace(bearer)
		if uid == "" {
			uid = "dev-user"
		}
		return &Session{UserID: uid, Email: uid + "@dev.local"}, nil
	}
}

// RequireAuth verifies the Authorization header on every request whose path
// isn't in publicPaths, delegating the actual check to verify. On success
// the resolved Session is attached to the request context (FromContext).
//
// Responses: 401 when no credential is provided or verification fails.
func RequireAuth(publicPaths []string, verify TokenVerifier) func(http.Handler) http.Handler {
	public := make(map[string]struct{}, len(publicPaths))
	for _, p := range publicPaths {
		public[p] = struct{}{}
	}
	if verify == nil {
		log.Print("auth: no TokenVerifier configured — falling back to the dev passthrough verifier (DO NOT use in production)")
		verify = NewDevVerifier()
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
