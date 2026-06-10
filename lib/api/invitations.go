package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/invitation"
	"github.com/spacefleet/spacefleet/lib/email"
	"github.com/spacefleet/spacefleet/lib/invitations"
)

// ListInvitations returns the organization's pending invitations. Admin-only.
func (s *Server) ListInvitations(ctx context.Context, _ ListInvitationsRequestObject) (ListInvitationsResponseObject, error) {
	if s.invites == nil {
		return errResp[ListInvitationsdefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "invitations service not configured"), nil
	}
	m, aerr, err := s.resolveAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListInvitationsdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	list, err := s.invites.ListPending(ctx, m.OrganizationID)
	if err != nil {
		return nil, err
	}
	out := make([]Invitation, 0, len(list))
	for _, inv := range list {
		out = append(out, toAPIInvitation(inv))
	}
	return ListInvitations200JSONResponse(out), nil
}

// CreateInvitation invites a user to the organization. Admin-only. It always
// returns a copy-able accept link; when email is configured it also enqueues an
// invitation email (email_sent reports whether it was enqueued).
func (s *Server) CreateInvitation(ctx context.Context, req CreateInvitationRequestObject) (CreateInvitationResponseObject, error) {
	if s.invites == nil {
		return errResp[CreateInvitationdefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "invitations service not configured"), nil
	}
	m, aerr, err := s.resolveAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[CreateInvitationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[CreateInvitationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	recipient := strings.TrimSpace(req.Body.Email)
	if recipient == "" {
		return errResp[CreateInvitationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "email is required"), nil
	}

	inv, token, err := s.invites.Create(ctx, m.OrganizationID, recipient, invitation.Role(req.Body.Role), m.UserID)
	if err != nil {
		return nil, err
	}

	// Only the token's hash is stored, so the accept link can be built only
	// here, from the plaintext token Create returned.
	acceptURL := s.externalURL + "/invite/" + token

	// Best-effort email on top of the link. Look up the org name for the body;
	// if that fails, skip the email rather than failing the request.
	emailSent := false
	if org, oerr := s.orgs.Get(ctx, m.OrganizationID); oerr == nil {
		emailSent = s.enqueueInviteEmail(ctx, emailArgs(inv, org.Name, acceptURL))
	}

	return CreateInvitation201JSONResponse(InvitationCreateResult{
		Invitation: toAPIInvitation(inv),
		AcceptUrl:  acceptURL,
		EmailSent:  emailSent,
	}), nil
}

// RevokeInvitation invalidates a pending invitation's link. Admin-only.
func (s *Server) RevokeInvitation(ctx context.Context, req RevokeInvitationRequestObject) (RevokeInvitationResponseObject, error) {
	if s.invites == nil {
		return errResp[RevokeInvitationdefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "invitations service not configured"), nil
	}
	m, aerr, err := s.resolveAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[RevokeInvitationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if err := s.invites.Revoke(ctx, m.OrganizationID, req.Id); err != nil {
		if ent.IsNotFound(err) {
			return errResp[RevokeInvitationdefaultJSONResponse](http.StatusNotFound, "not_found", "invitation not found"), nil
		}
		return nil, err
	}
	return RevokeInvitation204Response{}, nil
}

// GetInvitation previews an invitation by token for the accept screen. Requires
// a signed-in user but is not org-scoped — the token is the lookup key.
func (s *Server) GetInvitation(ctx context.Context, req GetInvitationRequestObject) (GetInvitationResponseObject, error) {
	if s.invites == nil {
		return errResp[GetInvitationdefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "invitations service not configured"), nil
	}
	if _, err := s.currentUser(ctx); err != nil {
		if errors.Is(err, errNoSession) {
			return errResp[GetInvitationdefaultJSONResponse](http.StatusUnauthorized, "unauthorized", "no session"), nil
		}
		return nil, err
	}
	inv, err := s.invites.Lookup(ctx, req.Token)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[GetInvitationdefaultJSONResponse](http.StatusNotFound, "not_found", "invitation not found"), nil
		}
		return nil, err
	}
	name := ""
	if inv.Edges.Organization != nil {
		name = inv.Edges.Organization.Name
	}
	return GetInvitation200JSONResponse(InvitationPreview{
		OrganizationName: name,
		Role:             Role(inv.Role),
		Status:           InvitationStatus(inv.Status),
		Expired:          time.Now().After(inv.ExpiresAt),
	}), nil
}

// AcceptInvitation joins the signed-in user to the invitation's organization
// with its role. Not org-scoped — the token is the sole authority.
func (s *Server) AcceptInvitation(ctx context.Context, req AcceptInvitationRequestObject) (AcceptInvitationResponseObject, error) {
	if s.invites == nil {
		return errResp[AcceptInvitationdefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "invitations service not configured"), nil
	}
	u, err := s.currentUser(ctx)
	if err != nil {
		if errors.Is(err, errNoSession) {
			return errResp[AcceptInvitationdefaultJSONResponse](http.StatusUnauthorized, "unauthorized", "no session"), nil
		}
		return nil, err
	}
	org, err := s.invites.Accept(ctx, req.Token, u.ID)
	if err != nil {
		switch {
		case ent.IsNotFound(err):
			return errResp[AcceptInvitationdefaultJSONResponse](http.StatusNotFound, "not_found", "invitation not found"), nil
		case errors.Is(err, invitations.ErrNotPending):
			return errResp[AcceptInvitationdefaultJSONResponse](http.StatusGone, "invitation_resolved", "this invitation is no longer available"), nil
		case errors.Is(err, invitations.ErrExpired):
			return errResp[AcceptInvitationdefaultJSONResponse](http.StatusGone, "invitation_expired", "this invitation has expired"), nil
		default:
			return nil, err
		}
	}
	return AcceptInvitation200JSONResponse(toAPIOrganization(org)), nil
}

// toAPIInvitation maps an invitation to the API type. The accept link is not
// part of it — only the token's hash is stored, so the link exists only in the
// create response.
func toAPIInvitation(inv *ent.Invitation) Invitation {
	return Invitation{
		Id:        inv.ID,
		Email:     inv.Email,
		Role:      Role(inv.Role),
		Status:    InvitationStatus(inv.Status),
		ExpiresAt: inv.ExpiresAt,
		CreatedAt: inv.CreatedAt,
	}
}

// emailArgs builds the invite-email job args from the invitation, the
// organization's name, and the accept link built at create time.
func emailArgs(inv *ent.Invitation, orgName, acceptURL string) email.InviteEmailArgs {
	return email.InviteEmailArgs{
		To:        inv.Email,
		OrgName:   orgName,
		Role:      string(inv.Role),
		AcceptURL: acceptURL,
	}
}
