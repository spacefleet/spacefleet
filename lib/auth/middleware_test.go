package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, _ := FromContext(r.Context())
		if sess == nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(sess.UserID))
	})
}

func TestRequireAuthPublicPathBypasses(t *testing.T) {
	rejectAll := func(context.Context, string) (*Session, error) { return nil, errors.New("nope") }
	h := RequireAuth([]string{"/api/health"}, rejectAll)(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Public path skips verification; the handler runs but FromContext is
	// empty, so okHandler writes 500. We only care that it wasn't a 401.
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("public path should not require auth, got %d", rec.Code)
	}
}

func TestRequireAuthRejectsInvalidToken(t *testing.T) {
	rejectAll := func(context.Context, string) (*Session, error) { return nil, errors.New("nope") }
	h := RequireAuth(nil, rejectAll)(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer bad")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a rejected token, got %d", rec.Code)
	}
}

func TestRequireAuthAttachesSession(t *testing.T) {
	verify := func(_ context.Context, bearer string) (*Session, error) {
		return &Session{UserID: bearer}, nil
	}
	h := RequireAuth(nil, verify)(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer user-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Body.String(); got != "user-123" {
		t.Fatalf("session user = %q, want user-123", got)
	}
}

func TestDevVerifierDefaultsUser(t *testing.T) {
	v := NewDevVerifier()
	sess, err := v(context.Background(), "")
	if err != nil {
		t.Fatalf("dev verifier: %v", err)
	}
	if sess.UserID != "dev-user" {
		t.Fatalf("dev user = %q, want dev-user", sess.UserID)
	}
}
