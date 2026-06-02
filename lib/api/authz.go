package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/membership"
)

// roleRank orders the membership roles so a single ">=" comparison expresses
// "at least this role". admin > editor > viewer.
var roleRank = map[membership.Role]int{
	membership.RoleViewer: 0,
	membership.RoleEditor: 1,
	membership.RoleAdmin:  2,
}

// atLeast reports whether role is at least as privileged as min.
func atLeast(role, min membership.Role) bool {
	return roleRank[role] >= roleRank[min]
}

// requireRole returns a 403 apiError when the membership's role is below min,
// or nil when it's sufficient. It's the role gate org-scoped mutations sit
// behind, on top of the membership check resolveMembership already did.
func requireRole(m *ent.Membership, min membership.Role) *apiError {
	if !atLeast(m.Role, min) {
		return &apiError{http.StatusForbidden, "forbidden", "your role does not permit this action"}
	}
	return nil
}

// resolveMembership runs the common org-scoped preamble: confirm the account
// services exist, resolve the authenticated user (provisioning on first sight),
// and resolve + authorize the target organization (from X-Organization-ID). It
// returns the caller's membership — from which org id and role are read — or a
// client error to render, or an internal error to bubble up. Handlers that
// mutate then gate on the role with requireRole; read handlers ignore it.
func (s *Server) resolveMembership(ctx context.Context) (*ent.Membership, *apiError, error) {
	if s.users == nil || s.orgs == nil {
		return nil, &apiError{http.StatusServiceUnavailable, "unavailable", "account service not configured"}, nil
	}
	u, err := s.currentUser(ctx)
	if err != nil {
		if errors.Is(err, errNoSession) {
			return nil, &apiError{http.StatusUnauthorized, "unauthorized", "no session"}, nil
		}
		return nil, nil, err
	}
	m, err := s.currentOrg(ctx, u.ID)
	if err != nil {
		switch {
		case errors.Is(err, errNoOrg):
			return nil, &apiError{http.StatusBadRequest, "bad_request", "no organization selected"}, nil
		case ent.IsNotFound(err):
			return nil, &apiError{http.StatusForbidden, "forbidden", "not a member of this organization"}, nil
		default:
			return nil, nil, err
		}
	}
	return m, nil, nil
}
