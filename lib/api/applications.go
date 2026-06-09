package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/lib/applications"
)

// resolveApp runs the read preamble for application handlers: confirm the
// applications service exists, then resolve + authorize org membership. Mirrors
// resolveOrg for clusters.
func (s *Server) resolveApp(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.applications == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "applications service not configured"}, nil
	}
	m, aerr, err := s.resolveMembership(ctx)
	if err != nil || aerr != nil {
		return uuid.Nil, aerr, err
	}
	return m.OrganizationID, nil, nil
}

// resolveAppRead is the read preamble plus the caller's "can see secret-bearing
// free-text" capability: a component's stored Helm `values` is not sealed at
// rest and may contain secrets, so it is only returned to editor-or-above
// callers. Used by the workflow/run handlers (lib/workflows config lives on
// components) to decide redaction.
func (s *Server) resolveAppRead(ctx context.Context) (uuid.UUID, bool, *apiError, error) {
	if s.applications == nil {
		return uuid.Nil, false, &apiError{http.StatusServiceUnavailable, "unavailable", "applications service not configured"}, nil
	}
	m, aerr, err := s.resolveMembership(ctx)
	if err != nil || aerr != nil {
		return uuid.Nil, false, aerr, err
	}
	return m.OrganizationID, atLeast(m.Role, membership.RoleEditor), nil, nil
}

// resolveAppWrite is resolveApp plus an editor-or-above gate, for the handlers
// that change state (create, update, delete, import).
func (s *Server) resolveAppWrite(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.applications == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "applications service not configured"}, nil
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

func (s *Server) ListApplications(ctx context.Context, _ ListApplicationsRequestObject) (ListApplicationsResponseObject, error) {
	orgID, aerr, err := s.resolveApp(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListApplicationsdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	list, err := s.applications.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Application, len(list))
	for i, a := range list {
		out[i] = toAPIApplication(a)
	}
	return ListApplications200JSONResponse(out), nil
}

func (s *Server) GetApplication(ctx context.Context, req GetApplicationRequestObject) (GetApplicationResponseObject, error) {
	orgID, aerr, err := s.resolveApp(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetApplicationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	a, err := s.applications.Get(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[GetApplicationdefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		}
		return nil, err
	}
	return GetApplication200JSONResponse(toAPIApplication(a)), nil
}

func (s *Server) CreateApplication(ctx context.Context, req CreateApplicationRequestObject) (CreateApplicationResponseObject, error) {
	orgID, aerr, err := s.resolveAppWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[CreateApplicationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[CreateApplicationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	name := strings.TrimSpace(req.Body.Name)
	if name == "" {
		return errResp[CreateApplicationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name is required"), nil
	}
	a, err := s.applications.Create(ctx, orgID, applications.CreateParams{
		Name:            name,
		TargetNamespace: strings.TrimSpace(req.Body.TargetNamespace),
		TargetClusterID: req.Body.TargetClusterId,
		RunnerClusterID: req.Body.RunnerClusterId,
		GroupID:         req.Body.GroupId,
	})
	if err != nil {
		if resp, ok := appWriteError[CreateApplicationdefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return CreateApplication201JSONResponse(toAPIApplication(a)), nil
}

// ImportApplication adopts a workload running on the target cluster as a managed
// application (the import flow). It creates the application in the imported
// state; the user then builds the deploy workflow from components. Editor or
// above.
func (s *Server) ImportApplication(ctx context.Context, req ImportApplicationRequestObject) (ImportApplicationResponseObject, error) {
	orgID, aerr, err := s.resolveAppWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ImportApplicationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[ImportApplicationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	name := strings.TrimSpace(req.Body.Name)
	if name == "" {
		return errResp[ImportApplicationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name is required"), nil
	}
	a, err := s.applications.Adopt(ctx, orgID, applications.ImportParams{
		Name:            name,
		TargetNamespace: strings.TrimSpace(req.Body.TargetNamespace),
		TargetClusterID: req.Body.TargetClusterId,
		RunnerClusterID: req.Body.RunnerClusterId,
		GroupID:         req.Body.GroupId,
	})
	if err != nil {
		if resp, ok := appWriteError[ImportApplicationdefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return ImportApplication202JSONResponse(toAPIApplication(a)), nil
}

func (s *Server) UpdateApplication(ctx context.Context, req UpdateApplicationRequestObject) (UpdateApplicationResponseObject, error) {
	orgID, aerr, err := s.resolveAppWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[UpdateApplicationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[UpdateApplicationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	var params applications.UpdateParams
	if req.Body.Name != nil {
		name := strings.TrimSpace(*req.Body.Name)
		if name == "" {
			return errResp[UpdateApplicationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name cannot be empty"), nil
		}
		params.Name = &name
	}
	if req.Body.TargetNamespace != nil {
		ns := strings.TrimSpace(*req.Body.TargetNamespace)
		params.TargetNamespace = &ns
	}
	a, err := s.applications.Update(ctx, orgID, req.Id, params)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[UpdateApplicationdefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		}
		if resp, ok := appWriteError[UpdateApplicationdefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return UpdateApplication200JSONResponse(toAPIApplication(a)), nil
}

func (s *Server) DeleteApplication(ctx context.Context, req DeleteApplicationRequestObject) (DeleteApplicationResponseObject, error) {
	orgID, aerr, err := s.resolveAppWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[DeleteApplicationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if err := s.applications.Delete(ctx, orgID, req.Id); err != nil {
		if ent.IsNotFound(err) {
			return errResp[DeleteApplicationdefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		}
		return nil, err
	}
	return DeleteApplication204Response{}, nil
}

// SetApplicationGroup moves an application into a group (folder) or back to the
// org root. The request's group_id is always present: a uuid moves it into that
// group, null removes it from any group. Editor or above.
func (s *Server) SetApplicationGroup(ctx context.Context, req SetApplicationGroupRequestObject) (SetApplicationGroupResponseObject, error) {
	orgID, aerr, err := s.resolveAppWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[SetApplicationGroupdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[SetApplicationGroupdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	// group_id is always present in the body: a uuid moves the app into that
	// group, nil (null/absent) removes it from any group.
	a, err := s.applications.SetGroup(ctx, orgID, req.Id, req.Body.GroupId)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[SetApplicationGroupdefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		}
		if resp, ok := appWriteError[SetApplicationGroupdefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return SetApplicationGroup200JSONResponse(toAPIApplication(a)), nil
}

// appWriteError maps service-layer write errors to a typed client error, or
// reports false to bubble up as 500.
func appWriteError[T defaultResp](err error) (T, bool) {
	switch {
	case applications.IsValidation(err):
		return errResp[T](http.StatusBadRequest, "bad_request", err.Error()), true
	case ent.IsConstraintError(err):
		return errResp[T](http.StatusConflict, "conflict", "an application with that name already exists in this organization"), true
	default:
		var zero T
		return zero, false
	}
}

func toAPIApplication(a *ent.Application) Application {
	imported := a.Imported
	out := Application{
		Id:              a.ID,
		Name:            a.Name,
		TargetNamespace: a.TargetNamespace,
		TargetClusterId: a.TargetClusterID,
		RunnerClusterId: a.RunnerClusterID,
		Imported:        &imported,
		CreatedAt:       a.CreatedAt,
		UpdatedAt:       a.UpdatedAt,
	}
	// group_id is an optional FK: uuid.Nil means the app sits at the org root.
	if a.GroupID != uuid.Nil {
		gid := a.GroupID
		out.GroupId = &gid
	}
	return out
}
