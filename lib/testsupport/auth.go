package testsupport

import (
	"context"
	"strings"

	"github.com/spacefleet/app/lib/auth"
)

// FakeVerifier returns a TokenVerifier for tests, standing in for the real Dex
// (OIDC) verifier so route- and handler-level tests don't need a live Dex. It
// performs no verification: the bearer token is used verbatim as the user's
// OIDC subject (so a distinct Authorization header is a distinct user), and an
// empty token resolves to "test-user".
//
// This lives in testsupport, not production code, on purpose — Spacefleet has
// no passthrough auth mode; the server fails closed without a real verifier.
func FakeVerifier() auth.TokenVerifier {
	return func(_ context.Context, bearer string) (*auth.Session, error) {
		uid := strings.TrimSpace(bearer)
		if uid == "" {
			uid = "test-user"
		}
		return &auth.Session{UserID: uid, Email: uid + "@test.local"}, nil
	}
}
