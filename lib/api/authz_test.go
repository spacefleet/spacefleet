package api

import (
	"testing"

	"github.com/spacefleet/spacefleet/ent/membership"
)

// TestAtLeast pins the role hierarchy admin > editor > viewer that the
// org-scoped role gates depend on.
func TestAtLeast(t *testing.T) {
	cases := []struct {
		role membership.Role
		min  membership.Role
		want bool
	}{
		{membership.RoleAdmin, membership.RoleAdmin, true},
		{membership.RoleAdmin, membership.RoleEditor, true},
		{membership.RoleAdmin, membership.RoleViewer, true},
		{membership.RoleEditor, membership.RoleAdmin, false},
		{membership.RoleEditor, membership.RoleEditor, true},
		{membership.RoleEditor, membership.RoleViewer, true},
		{membership.RoleViewer, membership.RoleAdmin, false},
		{membership.RoleViewer, membership.RoleEditor, false},
		{membership.RoleViewer, membership.RoleViewer, true},
	}
	for _, c := range cases {
		if got := atLeast(c.role, c.min); got != c.want {
			t.Errorf("atLeast(%q, %q) = %v, want %v", c.role, c.min, got, c.want)
		}
	}
}
