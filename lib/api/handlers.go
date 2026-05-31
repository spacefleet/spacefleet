package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/app/ent"
	"github.com/spacefleet/app/lib/auth"
	"github.com/spacefleet/app/lib/clusters"
	"github.com/spacefleet/app/lib/organizations"
	"github.com/spacefleet/app/lib/users"
)

type Server struct {
	users    *users.Service
	orgs     *organizations.Service
	clusters *clusters.Service
}

// NewServer accepts the runtime services this API depends on. They may be
// nil — a handler whose service is missing returns a clear "not configured"
// error instead of panicking, which keeps route-level tests usable without
// a database.
func NewServer(usersSvc *users.Service, orgsSvc *organizations.Service, clustersSvc *clusters.Service) *Server {
	return &Server{users: usersSvc, orgs: orgsSvc, clusters: clustersSvc}
}

var _ StrictServerInterface = (*Server)(nil)

// errNoSession indicates the request reached a handler without an authenticated
// session. RequireAuth should make this impossible, so it's a defensive 401.
var errNoSession = errors.New("api: no session on request")

// errNoOrg indicates an org-scoped request arrived without a usable
// X-Organization-ID. Handlers map it to 400.
var errNoOrg = errors.New("api: no organization selected")

func (s *Server) GetHealth(_ context.Context, _ GetHealthRequestObject) (GetHealthResponseObject, error) {
	return GetHealth200JSONResponse{Status: Ok}, nil
}

// GetMe returns the authenticated user (provisioning them on first sight) and
// the organizations they belong to. The SPA bootstraps from this.
func (s *Server) GetMe(ctx context.Context, _ GetMeRequestObject) (GetMeResponseObject, error) {
	if s.users == nil || s.orgs == nil {
		return errResp[GetMedefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "account service not configured"), nil
	}
	u, err := s.currentUser(ctx)
	if err != nil {
		if errors.Is(err, errNoSession) {
			return errResp[GetMedefaultJSONResponse](http.StatusUnauthorized, "unauthorized", "no session"), nil
		}
		return nil, err
	}
	mships, err := s.orgs.ListForUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	orgs := make([]OrgMembership, 0, len(mships))
	for _, m := range mships {
		if m.Edges.Organization == nil {
			continue
		}
		orgs = append(orgs, OrgMembership{
			Organization: toAPIOrganization(m.Edges.Organization),
			Role:         Role(m.Role),
		})
	}
	return GetMe200JSONResponse{
		User:          User{Id: u.ID, Email: u.Email},
		Organizations: orgs,
	}, nil
}

// CreateOrganization creates an organization owned by the current user.
func (s *Server) CreateOrganization(ctx context.Context, req CreateOrganizationRequestObject) (CreateOrganizationResponseObject, error) {
	if s.users == nil || s.orgs == nil {
		return errResp[CreateOrganizationdefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "account service not configured"), nil
	}
	u, err := s.currentUser(ctx)
	if err != nil {
		if errors.Is(err, errNoSession) {
			return errResp[CreateOrganizationdefaultJSONResponse](http.StatusUnauthorized, "unauthorized", "no session"), nil
		}
		return nil, err
	}
	name := ""
	if req.Body != nil {
		name = strings.TrimSpace(req.Body.Name)
	}
	if name == "" {
		return errResp[CreateOrganizationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name is required"), nil
	}
	org, err := s.orgs.Create(ctx, u.ID, name)
	if err != nil {
		return nil, err
	}
	return CreateOrganization201JSONResponse(toAPIOrganization(org)), nil
}

// RenameOrganization renames an organization the current user owns.
func (s *Server) RenameOrganization(ctx context.Context, req RenameOrganizationRequestObject) (RenameOrganizationResponseObject, error) {
	if s.users == nil || s.orgs == nil {
		return errResp[RenameOrganizationdefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "account service not configured"), nil
	}
	u, err := s.currentUser(ctx)
	if err != nil {
		if errors.Is(err, errNoSession) {
			return errResp[RenameOrganizationdefaultJSONResponse](http.StatusUnauthorized, "unauthorized", "no session"), nil
		}
		return nil, err
	}
	name := ""
	if req.Body != nil {
		name = strings.TrimSpace(req.Body.Name)
	}
	if name == "" {
		return errResp[RenameOrganizationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name is required"), nil
	}
	org, err := s.orgs.Rename(ctx, u.ID, req.Id, name)
	if err != nil {
		switch {
		case ent.IsNotFound(err):
			return errResp[RenameOrganizationdefaultJSONResponse](http.StatusNotFound, "not_found", "organization not found"), nil
		case errors.Is(err, organizations.ErrForbidden):
			return errResp[RenameOrganizationdefaultJSONResponse](http.StatusForbidden, "forbidden", "only owners can rename an organization"), nil
		default:
			return nil, err
		}
	}
	return RenameOrganization200JSONResponse(toAPIOrganization(org)), nil
}

// currentUser resolves the authenticated session to a local user record,
// provisioning it on first sight.
func (s *Server) currentUser(ctx context.Context) (*ent.User, error) {
	sess, ok := auth.FromContext(ctx)
	if !ok {
		return nil, errNoSession
	}
	return s.users.EnsureUser(ctx, sess.UserID, sess.Email)
}

// currentOrg resolves the organization the request targets (from the
// X-Organization-ID header, lifted onto the context by auth.OrgContext) and
// verifies the user belongs to it. It returns the caller's membership — from
// which the org id and role are read — or errNoOrg when no/invalid org was
// supplied, or ent's NotFoundError when the user isn't a member.
func (s *Server) currentOrg(ctx context.Context, userID uuid.UUID) (*ent.Membership, error) {
	raw, ok := auth.OrgIDFromContext(ctx)
	if !ok {
		return nil, errNoOrg
	}
	orgID, err := uuid.Parse(raw)
	if err != nil {
		return nil, errNoOrg
	}
	return s.orgs.Membership(ctx, userID, orgID)
}

func toAPIOrganization(o *ent.Organization) Organization {
	return Organization{
		Id:        o.ID,
		Name:      o.Name,
		CreatedAt: o.CreatedAt,
		UpdatedAt: o.UpdatedAt,
	}
}

// errResp is a small generic helper so each handler can return its specific
// typed default response without repeating the struct literal. T is bound
// to the per-operation `*defaultJSONResponse` struct.
type defaultResp interface {
	~struct {
		Body       Error
		StatusCode int
	}
}

func errResp[T defaultResp](status int, code, msg string) T {
	return T{Body: Error{Code: code, Message: msg}, StatusCode: status}
}
