//go:build integration

package users

import (
	"context"
	"testing"

	"github.com/spacefleet/app/lib/testsupport"
)

// TestEnsureUserIsIdempotent confirms the upsert keys on the OIDC subject:
// calling it twice for the same subject yields one row and refreshes the email.
func TestEnsureUserIsIdempotent(t *testing.T) {
	svc := NewService(testsupport.NewEntClient(t))
	ctx := context.Background()

	first, err := svc.EnsureUser(ctx, "sub-123", "old@example.com")
	if err != nil {
		t.Fatalf("first EnsureUser: %v", err)
	}

	second, err := svc.EnsureUser(ctx, "sub-123", "new@example.com")
	if err != nil {
		t.Fatalf("second EnsureUser: %v", err)
	}

	if first.ID != second.ID {
		t.Fatalf("expected same user id, got %s then %s", first.ID, second.ID)
	}
	if second.Email != "new@example.com" {
		t.Fatalf("email should be refreshed on re-login, got %q", second.Email)
	}

	// A different subject is a different user.
	other, err := svc.EnsureUser(ctx, "sub-456", "other@example.com")
	if err != nil {
		t.Fatalf("other EnsureUser: %v", err)
	}
	if other.ID == first.ID {
		t.Fatalf("distinct subjects must map to distinct users")
	}
}
