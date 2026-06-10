//go:build integration

package invitations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/invitation"
	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/lib/organizations"
	"github.com/spacefleet/spacefleet/lib/testsupport"
	"github.com/spacefleet/spacefleet/lib/users"
)

// TestCreateAcceptAndSupersede covers the invitation lifecycle: creating an
// invite supersedes any prior pending one for the same address; accepting joins
// the user with the invite's role and marks it accepted; re-accepting is gone.
func TestCreateAcceptAndSupersede(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	orgSvc := organizations.NewService(client)
	userSvc := users.NewService(client)
	ctx := context.Background()

	admin, _ := userSvc.EnsureUser(ctx, "admin", "admin@example.com")
	invitee, _ := userSvc.EnsureUser(ctx, "invitee", "invitee@example.com")
	org, err := orgSvc.Create(ctx, admin.ID, "Acme")
	if err != nil {
		t.Fatalf("create org: %v", err)
	}

	// First invite, then a second for the same email supersedes it.
	_, firstToken, err := svc.Create(ctx, org.ID, "invitee@example.com", invitation.RoleViewer, admin.ID)
	if err != nil {
		t.Fatalf("create first invite: %v", err)
	}
	second, secondToken, err := svc.Create(ctx, org.ID, "invitee@example.com", invitation.RoleEditor, admin.ID)
	if err != nil {
		t.Fatalf("create second invite: %v", err)
	}

	pending, err := svc.ListPending(ctx, org.ID)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != second.ID {
		t.Fatalf("expected only the second invite pending, got %d", len(pending))
	}

	// The superseded first token no longer accepts.
	if _, err := svc.Accept(ctx, firstToken, invitee.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("accept superseded: got %v, want ErrNotPending", err)
	}

	// Accepting the live invite joins the user as an editor.
	joined, err := svc.Accept(ctx, secondToken, invitee.ID)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if joined.ID != org.ID {
		t.Fatalf("accepted into wrong org")
	}
	m, err := orgSvc.Membership(ctx, invitee.ID, org.ID)
	if err != nil {
		t.Fatalf("membership after accept: %v", err)
	}
	if m.Role != membership.RoleEditor {
		t.Fatalf("joined role = %q, want editor", m.Role)
	}

	// Re-accepting the now-accepted invite is gone.
	if _, err := svc.Accept(ctx, secondToken, invitee.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("re-accept: got %v, want ErrNotPending", err)
	}
}

// TestAcceptExpired confirms an expired invitation can't be accepted.
func TestAcceptExpired(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	orgSvc := organizations.NewService(client)
	userSvc := users.NewService(client)
	ctx := context.Background()

	admin, _ := userSvc.EnsureUser(ctx, "admin", "admin@example.com")
	invitee, _ := userSvc.EnsureUser(ctx, "invitee", "invitee@example.com")
	org, _ := orgSvc.Create(ctx, admin.ID, "Acme")

	inv, token, err := svc.Create(ctx, org.ID, "invitee@example.com", invitation.RoleViewer, admin.ID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Force expiry into the past.
	if err := client.Invitation.UpdateOneID(inv.ID).
		SetExpiresAt(time.Now().Add(-time.Hour)).
		Exec(ctx); err != nil {
		t.Fatalf("expire: %v", err)
	}

	if _, err := svc.Accept(ctx, token, invitee.ID); !errors.Is(err, ErrExpired) {
		t.Fatalf("accept expired: got %v, want ErrExpired", err)
	}
}

// TestTokenStoredHashed is the regression test for storing only a hash of the
// invite token: the database row must not contain the plaintext token (a DB or
// backup read must not yield a live invite link), the plaintext must still look
// up, and the stored hash itself must not be usable as a token.
func TestTokenStoredHashed(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	orgSvc := organizations.NewService(client)
	userSvc := users.NewService(client)
	ctx := context.Background()

	admin, _ := userSvc.EnsureUser(ctx, "admin", "admin@example.com")
	org, _ := orgSvc.Create(ctx, admin.ID, "Acme")

	inv, token, err := svc.Create(ctx, org.ID, "invitee@example.com", invitation.RoleViewer, admin.ID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	row := client.Invitation.GetX(ctx, inv.ID)
	if row.TokenHash == token {
		t.Fatal("plaintext token stored in the database")
	}
	sum := sha256.Sum256([]byte(token))
	if want := hex.EncodeToString(sum[:]); row.TokenHash != want {
		t.Fatalf("stored token_hash = %q, want sha256 hex of the token", row.TokenHash)
	}

	// The plaintext token looks up; the stored hash is not itself a token.
	if _, err := svc.Lookup(ctx, token); err != nil {
		t.Fatalf("lookup by plaintext token: %v", err)
	}
	if _, err := svc.Lookup(ctx, row.TokenHash); !ent.IsNotFound(err) {
		t.Fatalf("lookup by stored hash: got %v, want not-found", err)
	}
}
