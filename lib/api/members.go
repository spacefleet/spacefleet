package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/lib/organizations"
)

// ListMembers returns the current organization's members. Any member may view
// the roster (it's part of viewing the org).
func (s *Server) ListMembers(ctx context.Context, _ ListMembersRequestObject) (ListMembersResponseObject, error) {
	m, aerr, err := s.resolveMembership(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListMembersdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	list, err := s.orgs.ListMembers(ctx, m.OrganizationID)
	if err != nil {
		return nil, err
	}
	out := make([]Member, 0, len(list))
	for _, mem := range list {
		out = append(out, toAPIMember(mem))
	}
	return ListMembers200JSONResponse(out), nil
}

// UpdateMemberRole changes a member's role. Admin-only; the last admin can't be
// demoted (409).
func (s *Server) UpdateMemberRole(ctx context.Context, req UpdateMemberRoleRequestObject) (UpdateMemberRoleResponseObject, error) {
	m, aerr, err := s.resolveAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[UpdateMemberRoledefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[UpdateMemberRoledefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	updated, err := s.orgs.SetMemberRole(ctx, m.OrganizationID, req.UserId, membership.Role(req.Body.Role))
	if err != nil {
		switch {
		case ent.IsNotFound(err):
			return errResp[UpdateMemberRoledefaultJSONResponse](http.StatusNotFound, "not_found", "member not found"), nil
		case errors.Is(err, organizations.ErrLastAdmin):
			return errResp[UpdateMemberRoledefaultJSONResponse](http.StatusConflict, "last_admin", "the organization must keep at least one admin"), nil
		default:
			return nil, err
		}
	}
	// The updated membership doesn't eager-load its user; reuse the request's
	// known user id and the looked-up email.
	return UpdateMemberRole200JSONResponse(toAPIMember(updated)), nil
}

// RemoveMember removes a member from the organization. Admin-only; the last
// admin can't be removed (409).
func (s *Server) RemoveMember(ctx context.Context, req RemoveMemberRequestObject) (RemoveMemberResponseObject, error) {
	m, aerr, err := s.resolveAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[RemoveMemberdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if err := s.orgs.RemoveMember(ctx, m.OrganizationID, req.UserId); err != nil {
		switch {
		case ent.IsNotFound(err):
			return errResp[RemoveMemberdefaultJSONResponse](http.StatusNotFound, "not_found", "member not found"), nil
		case errors.Is(err, organizations.ErrLastAdmin):
			return errResp[RemoveMemberdefaultJSONResponse](http.StatusConflict, "last_admin", "the organization must keep at least one admin"), nil
		default:
			return nil, err
		}
	}
	return RemoveMember204Response{}, nil
}

// resolveAdmin is resolveMembership plus an admin role gate, for the
// organization-management handlers (members, invitations).
func (s *Server) resolveAdmin(ctx context.Context) (*ent.Membership, *apiError, error) {
	m, aerr, err := s.resolveMembership(ctx)
	if err != nil || aerr != nil {
		return nil, aerr, err
	}
	if aerr := requireRole(m, membership.RoleAdmin); aerr != nil {
		return nil, aerr, nil
	}
	return m, nil, nil
}

// toAPIMember maps a membership (with its user eager-loaded) to the API type.
// When the user edge isn't loaded (e.g. after an update) the email is omitted.
func toAPIMember(m *ent.Membership) Member {
	out := Member{
		UserId:    m.UserID,
		Role:      Role(m.Role),
		CreatedAt: m.CreatedAt,
	}
	if m.Edges.User != nil {
		out.Email = m.Edges.User.Email
	}
	return out
}
