package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/lib/chartcredentials"
	"github.com/spacefleet/spacefleet/lib/secrets"
)

// resolveChartCredentialsRead runs the read preamble for chart-credential
// handlers: confirm the service exists, then resolve + authorize org membership.
// Mirrors resolveOrg for clusters.
func (s *Server) resolveChartCredentialsRead(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.chartCredentials == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "chart credentials service not configured"}, nil
	}
	m, aerr, err := s.resolveMembership(ctx)
	if err != nil || aerr != nil {
		return uuid.Nil, aerr, err
	}
	return m.OrganizationID, nil, nil
}

// resolveChartCredentialsWrite is the read preamble plus an editor-or-above gate,
// for the handlers that change state (create, update, delete).
func (s *Server) resolveChartCredentialsWrite(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.chartCredentials == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "chart credentials service not configured"}, nil
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

func (s *Server) ListChartCredentials(ctx context.Context, _ ListChartCredentialsRequestObject) (ListChartCredentialsResponseObject, error) {
	orgID, aerr, err := s.resolveChartCredentialsRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListChartCredentialsdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	list, err := s.chartCredentials.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]ChartCredential, len(list))
	for i, c := range list {
		out[i] = toAPIChartCredential(c)
	}
	return ListChartCredentials200JSONResponse(out), nil
}

func (s *Server) GetChartCredential(ctx context.Context, req GetChartCredentialRequestObject) (GetChartCredentialResponseObject, error) {
	orgID, aerr, err := s.resolveChartCredentialsRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetChartCredentialdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	c, err := s.chartCredentials.Get(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[GetChartCredentialdefaultJSONResponse](http.StatusNotFound, "not_found", "chart credential not found"), nil
		}
		return nil, err
	}
	return GetChartCredential200JSONResponse(toAPIChartCredential(c)), nil
}

func (s *Server) CreateChartCredential(ctx context.Context, req CreateChartCredentialRequestObject) (CreateChartCredentialResponseObject, error) {
	orgID, aerr, err := s.resolveChartCredentialsWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[CreateChartCredentialdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[CreateChartCredentialdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	name := strings.TrimSpace(req.Body.Name)
	if name == "" {
		return errResp[CreateChartCredentialdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name is required"), nil
	}
	c, err := s.chartCredentials.Create(ctx, orgID, chartcredentials.CreateParams{
		Name:     name,
		Type:     string(req.Body.Type),
		Username: deref(req.Body.Username),
		Password: req.Body.Password,
	})
	if err != nil {
		if resp, ok := chartCredentialWriteError[CreateChartCredentialdefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return CreateChartCredential201JSONResponse(toAPIChartCredential(c)), nil
}

func (s *Server) UpdateChartCredential(ctx context.Context, req UpdateChartCredentialRequestObject) (UpdateChartCredentialResponseObject, error) {
	orgID, aerr, err := s.resolveChartCredentialsWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[UpdateChartCredentialdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[UpdateChartCredentialdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	params := chartcredentials.UpdateParams{
		Username: req.Body.Username,
		Password: req.Body.Password,
	}
	if req.Body.Name != nil {
		name := strings.TrimSpace(*req.Body.Name)
		if name == "" {
			return errResp[UpdateChartCredentialdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name cannot be empty"), nil
		}
		params.Name = &name
	}
	c, err := s.chartCredentials.Update(ctx, orgID, req.Id, params)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[UpdateChartCredentialdefaultJSONResponse](http.StatusNotFound, "not_found", "chart credential not found"), nil
		}
		if resp, ok := chartCredentialWriteError[UpdateChartCredentialdefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return UpdateChartCredential200JSONResponse(toAPIChartCredential(c)), nil
}

func (s *Server) DeleteChartCredential(ctx context.Context, req DeleteChartCredentialRequestObject) (DeleteChartCredentialResponseObject, error) {
	orgID, aerr, err := s.resolveChartCredentialsWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[DeleteChartCredentialdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if err := s.chartCredentials.Delete(ctx, orgID, req.Id); err != nil {
		if ent.IsNotFound(err) {
			return errResp[DeleteChartCredentialdefaultJSONResponse](http.StatusNotFound, "not_found", "chart credential not found"), nil
		}
		// The FK from applications is ON DELETE RESTRICT: a credential in use can't
		// be deleted (the service classifies the DB violation as ErrInUse).
		if errors.Is(err, chartcredentials.ErrInUse) {
			return errResp[DeleteChartCredentialdefaultJSONResponse](http.StatusConflict, "conflict", "this credential is attached to an application; detach it first"), nil
		}
		return nil, err
	}
	return DeleteChartCredential204Response{}, nil
}

// chartCredentialWriteError maps service-layer write errors common to
// create/update to a typed client error, or reports false to bubble up as 500.
func chartCredentialWriteError[T defaultResp](err error) (T, bool) {
	switch {
	case chartcredentials.IsValidation(err):
		return errResp[T](http.StatusBadRequest, "bad_request", err.Error()), true
	case errors.Is(err, secrets.ErrDisabled):
		return errResp[T](http.StatusBadRequest, "encryption_unavailable", "cannot store a chart credential without an encryption key — set SPACEFLEET_SECRET_KEY"), true
	case ent.IsConstraintError(err):
		return errResp[T](http.StatusConflict, "conflict", "a chart credential with that name already exists in this organization"), true
	default:
		var zero T
		return zero, false
	}
}

// toAPIChartCredential maps an ent row to the API type. It never exposes the
// sealed password — only the non-secret name, type, and username.
func toAPIChartCredential(c *ent.ChartCredential) ChartCredential {
	out := ChartCredential{
		Id:        c.ID,
		Name:      c.Name,
		Type:      ChartCredentialType(c.Type),
		Username:  optStr(c.Username),
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
	return out
}
