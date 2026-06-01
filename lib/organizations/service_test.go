//go:build integration

package organizations

import (
	"context"
	"errors"
	"testing"

	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/lib/testsupport"
	"github.com/spacefleet/spacefleet/lib/users"
)

// TestCreateAndListForUser exercises the core organization use cases against a
// real database: creating an org makes the creator its owner, listing returns
// only the caller's orgs, and renaming is owner-restricted.
func TestCreateAndListForUser(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	userSvc := users.NewService(client)
	ctx := context.Background()

	alice, err := userSvc.EnsureUser(ctx, "alice", "alice@example.com")
	if err != nil {
		t.Fatalf("ensure alice: %v", err)
	}
	bob, err := userSvc.EnsureUser(ctx, "bob", "bob@example.com")
	if err != nil {
		t.Fatalf("ensure bob: %v", err)
	}

	org, err := svc.Create(ctx, alice.ID, "Acme")
	if err != nil {
		t.Fatalf("create org: %v", err)
	}

	// Alice belongs to the org as its owner.
	mships, err := svc.ListForUser(ctx, alice.ID)
	if err != nil {
		t.Fatalf("list for alice: %v", err)
	}
	if len(mships) != 1 {
		t.Fatalf("alice should have 1 membership, got %d", len(mships))
	}
	if mships[0].Role != membership.RoleOwner {
		t.Fatalf("creator role = %q, want owner", mships[0].Role)
	}
	if mships[0].Edges.Organization == nil || mships[0].Edges.Organization.ID != org.ID {
		t.Fatalf("membership should eager-load the created org")
	}

	// Bob is isolated — he sees nothing.
	bobMships, err := svc.ListForUser(ctx, bob.ID)
	if err != nil {
		t.Fatalf("list for bob: %v", err)
	}
	if len(bobMships) != 0 {
		t.Fatalf("bob should have no memberships, got %d", len(bobMships))
	}

	// Bob can't rename an org he doesn't belong to (NotFound).
	if _, err := svc.Rename(ctx, bob.ID, org.ID, "Hacked"); err == nil {
		t.Fatalf("expected error renaming as non-member")
	}

	// Alice (owner) can rename it.
	renamed, err := svc.Rename(ctx, alice.ID, org.ID, "Acme Corp")
	if err != nil {
		t.Fatalf("owner rename: %v", err)
	}
	if renamed.Name != "Acme Corp" {
		t.Fatalf("rename: got %q, want Acme Corp", renamed.Name)
	}
}

// TestRenameForbiddenForNonOwner confirms a plain member can't rename the org.
func TestRenameForbiddenForNonOwner(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	userSvc := users.NewService(client)
	ctx := context.Background()

	owner, _ := userSvc.EnsureUser(ctx, "owner", "owner@example.com")
	member, _ := userSvc.EnsureUser(ctx, "member", "member@example.com")

	org, err := svc.Create(ctx, owner.ID, "Acme")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Add the second user as a plain member directly.
	if _, err := client.Membership.Create().
		SetUserID(member.ID).
		SetOrganizationID(org.ID).
		SetRole(membership.RoleMember).
		Save(ctx); err != nil {
		t.Fatalf("add member: %v", err)
	}

	_, err = svc.Rename(ctx, member.ID, org.ID, "Nope")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("member rename: got %v, want ErrForbidden", err)
	}
}
