// Package auth holds request authentication. The HTTP layer calls RequireAuth
// with a TokenVerifier, and a verifier turns a bearer token into a *Session.
//
// Spacefleet authenticates with its bundled Dex (OIDC): the verifier validates
// Dex-issued ID tokens (signature via JWKS, iss/exp/aud) and returns the token
// subject as Session.UserID — see oidc.go. There is no passthrough or
// allow-everyone mode; RequireAuth fails closed when no verifier is configured,
// and the server refuses to boot without an OIDC issuer.
package auth

import (
	"context"
	"net/http"
)

// Session is the identity surfaced to handlers. UserID is the OIDC subject
// (stable, unique per user); Email is the email claim, used when provisioning
// the local user record. Add more fields (groups, etc.) as the Dex claims you
// rely on solidify.
type Session struct {
	UserID string
	Email  string
}

type contextKey int

const sessionKey contextKey = 1

// WithSession stores a session on ctx so FromContext and downstream
// handlers can read it.
func WithSession(ctx context.Context, sess *Session) context.Context {
	return context.WithValue(ctx, sessionKey, sess)
}

// FromContext returns the authenticated session, or (nil, false) if the
// request was not authenticated.
func FromContext(ctx context.Context) (*Session, bool) {
	s, ok := ctx.Value(sessionKey).(*Session)
	if !ok || s == nil {
		return nil, false
	}
	return s, true
}

// writeJSONError writes a minimal JSON error body matching the OpenAPI
// Error schema. Kept small on purpose.
func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"code":"` + code + `","message":"` + msg + `"}`))
}
