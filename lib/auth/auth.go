// Package auth holds request authentication. It is intentionally
// provider-agnostic: the HTTP layer calls RequireAuth with a TokenVerifier,
// and a verifier turns a bearer token into a *Session.
//
// We plan to authenticate with Dex (OIDC). Wiring that up means
// implementing a TokenVerifier that validates Dex-issued ID/access tokens
// (typically via OIDC discovery + JWKS) and returning the subject as
// Session.UserID. Until that lands, NewDevVerifier provides a passthrough
// for local development — see middleware.go.
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
