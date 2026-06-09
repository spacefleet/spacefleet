package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/lib/applicationgroups"
)

// resolveGroup runs the read preamble for application-group handlers: confirm
// the service exists, then resolve + authorize org membership. Mirrors
// resolveApp.
func (s *Server) resolveGroup(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.applicationGroups == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "application groups service not configured"}, nil
	}
	m, aerr, err := s.resolveMembership(ctx)
	if err != nil || aerr != nil {
		return uuid.Nil, aerr, err
	}
	return m.OrganizationID, nil, nil
}

// resolveGroupWrite is resolveGroup plus an editor-or-above gate, for the
// handlers that change state (create, rename, delete).
func (s *Server) resolveGroupWrite(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.applicationGroups == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "application groups service not configured"}, nil
	}
	m, aerr, err := s.resolveMembership(ctx)
	if err != nil || aerr != nil {
		return uuid.Nil, aerr, err
	}
	if aerr := requireRole(m, membership.RoleEditor); aerr != nil {
		return uuid.Nil, aerr, nil
	}
	return m.OrganizationID, nil, nil
}

func (s *Server) ListApplicationGroups(ctx context.Context, _ ListApplicationGroupsRequestObject) (ListApplicationGroupsResponseObject, error) {
	orgID, aerr, err := s.resolveGroup(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListApplicationGroupsdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	list, err := s.applicationGroups.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]ApplicationGroup, len(list))
	for i, g := range list {
		out[i] = toAPIApplicationGroup(g)
	}
	return ListApplicationGroups200JSONResponse(out), nil
}

func (s *Server) GetApplicationGroup(ctx context.Context, req GetApplicationGroupRequestObject) (GetApplicationGroupResponseObject, error) {
	orgID, aerr, err := s.resolveGroup(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetApplicationGroupdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	g, err := s.applicationGroups.Get(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[GetApplicationGroupdefaultJSONResponse](http.StatusNotFound, "not_found", "application group not found"), nil
		}
		return nil, err
	}
	return GetApplicationGroup200JSONResponse(toAPIApplicationGroup(g)), nil
}

func (s *Server) CreateApplicationGroup(ctx context.Context, req CreateApplicationGroupRequestObject) (CreateApplicationGroupResponseObject, error) {
	orgID, aerr, err := s.resolveGroupWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[CreateApplicationGroupdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[CreateApplicationGroupdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	name := strings.TrimSpace(req.Body.Name)
	if name == "" {
		return errResp[CreateApplicationGroupdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name is required"), nil
	}
	g, err := s.applicationGroups.Create(ctx, orgID, name)
	if err != nil {
		if resp, ok := groupWriteError[CreateApplicationGroupdefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return CreateApplicationGroup201JSONResponse(toAPIApplicationGroup(g)), nil
}

func (s *Server) UpdateApplicationGroup(ctx context.Context, req UpdateApplicationGroupRequestObject) (UpdateApplicationGroupResponseObject, error) {
	orgID, aerr, err := s.resolveGroupWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[UpdateApplicationGroupdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[UpdateApplicationGroupdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	var params applicationgroups.UpdateParams
	if req.Body.Name != nil {
		name := strings.TrimSpace(*req.Body.Name)
		if name == "" {
			return errResp[UpdateApplicationGroupdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name cannot be empty"), nil
		}
		params.Name = &name
	}
	g, err := s.applicationGroups.Update(ctx, orgID, req.Id, params)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[UpdateApplicationGroupdefaultJSONResponse](http.StatusNotFound, "not_found", "application group not found"), nil
		}
		if resp, ok := groupWriteError[UpdateApplicationGroupdefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return UpdateApplicationGroup200JSONResponse(toAPIApplicationGroup(g)), nil
}

func (s *Server) DeleteApplicationGroup(ctx context.Context, req DeleteApplicationGroupRequestObject) (DeleteApplicationGroupResponseObject, error) {
	orgID, aerr, err := s.resolveGroupWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[DeleteApplicationGroupdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if err := s.applicationGroups.Delete(ctx, orgID, req.Id); err != nil {
		if ent.IsNotFound(err) {
			return errResp[DeleteApplicationGroupdefaultJSONResponse](http.StatusNotFound, "not_found", "application group not found"), nil
		}
		return nil, err
	}
	return DeleteApplicationGroup204Response{}, nil
}

// groupWriteError maps service-layer write errors to a typed client error, or
// reports false to bubble up as 500.
func groupWriteError[T defaultResp](err error) (T, bool) {
	switch {
	case applicationgroups.IsValidation(err):
		return errResp[T](http.StatusBadRequest, "bad_request", err.Error()), true
	case ent.IsConstraintError(err):
		return errResp[T](http.StatusConflict, "conflict", "an application group with that name already exists in this organization"), true
	default:
		var zero T
		return zero, false
	}
}

func toAPIApplicationGroup(g *ent.ApplicationGroup) ApplicationGroup {
	return ApplicationGroup{
		Id:        g.ID,
		Name:      g.Name,
		CreatedAt: g.CreatedAt,
		UpdatedAt: g.UpdatedAt,
	}
}
