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
// real database: creating an org makes the creator its admin, listing returns
// only the caller's orgs, and renaming is admin-restricted.
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

	// Alice belongs to the org as its admin.
	mships, err := svc.ListForUser(ctx, alice.ID)
	if err != nil {
		t.Fatalf("list for alice: %v", err)
	}
	if len(mships) != 1 {
		t.Fatalf("alice should have 1 membership, got %d", len(mships))
	}
	if mships[0].Role != membership.RoleAdmin {
		t.Fatalf("creator role = %q, want admin", mships[0].Role)
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

	// Alice (admin) can rename it.
	renamed, err := svc.Rename(ctx, alice.ID, org.ID, "Acme Corp")
	if err != nil {
		t.Fatalf("admin rename: %v", err)
	}
	if renamed.Name != "Acme Corp" {
		t.Fatalf("rename: got %q, want Acme Corp", renamed.Name)
	}
}

// TestRenameForbiddenForNonAdmin confirms a non-admin member can't rename the org.
func TestRenameForbiddenForNonAdmin(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	userSvc := users.NewService(client)
	ctx := context.Background()

	admin, _ := userSvc.EnsureUser(ctx, "admin", "admin@example.com")
	member, _ := userSvc.EnsureUser(ctx, "member", "member@example.com")

	org, err := svc.Create(ctx, admin.ID, "Acme")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Add the second user as an editor directly.
	if _, err := client.Membership.Create().
		SetUserID(member.ID).
		SetOrganizationID(org.ID).
		SetRole(membership.RoleEditor).
		Save(ctx); err != nil {
		t.Fatalf("add member: %v", err)
	}

	_, err = svc.Rename(ctx, member.ID, org.ID, "Nope")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("member rename: got %v, want ErrForbidden", err)
	}
}

// TestMemberManagementGuardsLastAdmin covers listing members, changing roles,
// removing members, and the invariant that an org always keeps one admin.
func TestMemberManagementGuardsLastAdmin(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	userSvc := users.NewService(client)
	ctx := context.Background()

	admin, _ := userSvc.EnsureUser(ctx, "admin", "admin@example.com")
	member, _ := userSvc.EnsureUser(ctx, "member", "member@example.com")

	org, err := svc.Create(ctx, admin.ID, "Acme")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := client.Membership.Create().
		SetUserID(member.ID).
		SetOrganizationID(org.ID).
		SetRole(membership.RoleViewer).
		Save(ctx); err != nil {
		t.Fatalf("add member: %v", err)
	}

	// ListMembers returns both, with users eager-loaded.
	members, err := svc.ListMembers(ctx, org.ID)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}
	for _, m := range members {
		if m.Edges.User == nil {
			t.Fatalf("ListMembers should eager-load the user edge")
		}
	}

	// The sole admin can't be demoted.
	if _, err := svc.SetMemberRole(ctx, org.ID, admin.ID, membership.RoleEditor); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote last admin: got %v, want ErrLastAdmin", err)
	}
	// ...nor removed.
	if err := svc.RemoveMember(ctx, org.ID, admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("remove last admin: got %v, want ErrLastAdmin", err)
	}

	// Promote the viewer to admin; now the original admin can be demoted.
	if _, err := svc.SetMemberRole(ctx, org.ID, member.ID, membership.RoleAdmin); err != nil {
		t.Fatalf("promote member: %v", err)
	}
	if _, err := svc.SetMemberRole(ctx, org.ID, admin.ID, membership.RoleEditor); err != nil {
		t.Fatalf("demote with another admin present: %v", err)
	}

	// Removing a non-admin is always fine.
	if err := svc.RemoveMember(ctx, org.ID, admin.ID); err != nil {
		t.Fatalf("remove non-admin: %v", err)
	}
}
