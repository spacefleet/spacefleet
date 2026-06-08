package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/lib/secrets"
	"github.com/spacefleet/spacefleet/lib/variables"
)

// variablesPreamble runs the common preamble for every variable handler: confirm
// the variables + applications services exist, resolve + authorize membership
// (editor-or-above for writes), and confirm the application belongs to the org
// (so a variable can't be hung off another org's app). Returns the org id.
func (s *Server) variablesPreamble(ctx context.Context, appID uuid.UUID, write bool) (uuid.UUID, *apiError, error) {
	if s.variables == nil || s.applications == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "variables service not configured"}, nil
	}
	m, aerr, err := s.resolveMembership(ctx)
	if err != nil || aerr != nil {
		return uuid.Nil, aerr, err
	}
	if write {
		if aerr := requireRole(m, membership.RoleEditor); aerr != nil {
			return uuid.Nil, aerr, nil
		}
	}
	orgID := m.OrganizationID
	if _, err := s.applications.Get(ctx, orgID, appID); err != nil {
		if ent.IsNotFound(err) {
			return uuid.Nil, &apiError{http.StatusNotFound, "not_found", "application not found"}, nil
		}
		return uuid.Nil, nil, err
	}
	return orgID, nil, nil
}

// listVariables, createVariable, updateVariable, deleteVariable hold the logic
// shared by the app-level (componentID nil) and component-level handlers; the
// per-operation StrictServerInterface methods below are thin wrappers that pass
// the right scope and render the typed response.

func (s *Server) listVariables(ctx context.Context, appID uuid.UUID, componentID *uuid.UUID) ([]Variable, *apiError, error) {
	orgID, aerr, err := s.variablesPreamble(ctx, appID, false)
	if err != nil || aerr != nil {
		return nil, aerr, err
	}
	rows, err := s.variables.List(ctx, orgID, appID, componentID)
	if err != nil {
		return nil, nil, err
	}
	out := make([]Variable, len(rows))
	for i, v := range rows {
		out[i] = toAPIVariable(v)
	}
	return out, nil, nil
}

func (s *Server) createVariable(ctx context.Context, appID uuid.UUID, componentID *uuid.UUID, body *VariableCreateRequest) (*Variable, *apiError, error) {
	orgID, aerr, err := s.variablesPreamble(ctx, appID, true)
	if err != nil || aerr != nil {
		return nil, aerr, err
	}
	if body == nil {
		return nil, &apiError{http.StatusBadRequest, "bad_request", "request body is required"}, nil
	}
	v, err := s.variables.Create(ctx, orgID, appID, componentID, variables.CreateParams{
		Name:      strings.TrimSpace(body.Name),
		Sensitive: body.Sensitive,
		Value:     body.Value,
	})
	if err != nil {
		aerr, ierr := variableWriteAPIError(err)
		return nil, aerr, ierr
	}
	out := toAPIVariable(v)
	return &out, nil, nil
}

func (s *Server) updateVariable(ctx context.Context, appID uuid.UUID, componentID *uuid.UUID, id uuid.UUID, body *VariableUpdateRequest) (*Variable, *apiError, error) {
	orgID, aerr, err := s.variablesPreamble(ctx, appID, true)
	if err != nil || aerr != nil {
		return nil, aerr, err
	}
	if body == nil {
		return nil, &apiError{http.StatusBadRequest, "bad_request", "request body is required"}, nil
	}
	params := variables.UpdateParams{Value: body.Value}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		params.Name = &name
	}
	v, err := s.variables.Update(ctx, orgID, appID, componentID, id, params)
	if err != nil {
		aerr, ierr := variableWriteAPIError(err)
		return nil, aerr, ierr
	}
	out := toAPIVariable(v)
	return &out, nil, nil
}

func (s *Server) deleteVariable(ctx context.Context, appID uuid.UUID, componentID *uuid.UUID, id uuid.UUID) (*apiError, error) {
	orgID, aerr, err := s.variablesPreamble(ctx, appID, true)
	if err != nil || aerr != nil {
		return aerr, err
	}
	if err := s.variables.Delete(ctx, orgID, appID, componentID, id); err != nil {
		if ent.IsNotFound(err) {
			return &apiError{http.StatusNotFound, "not_found", "variable not found"}, nil
		}
		return nil, err
	}
	return nil, nil
}

// variableWriteAPIError maps a create/update service error to a typed client
// error (returned as the *apiError to render), or returns it as a bubbling
// internal error (the error return) when it's not a known client error.
func variableWriteAPIError(err error) (*apiError, error) {
	switch {
	case variables.IsValidation(err):
		return &apiError{http.StatusBadRequest, "bad_request", err.Error()}, nil
	case errors.Is(err, secrets.ErrDisabled):
		return &apiError{http.StatusBadRequest, "encryption_unavailable", "cannot store a sensitive variable without an encryption key — set SPACEFLEET_SECRET_KEY"}, nil
	case ent.IsNotFound(err):
		return &apiError{http.StatusNotFound, "not_found", "variable not found"}, nil
	default:
		return nil, err
	}
}

// toAPIVariable maps an ent row to the API type. It never exposes the sealed
// value: a sensitive variable returns only its name + the sensitive flag; a
// non-secret one also returns its plaintext value.
func toAPIVariable(v *ent.Variable) Variable {
	out := Variable{
		Id:        v.ID,
		Name:      v.Name,
		Sensitive: v.Sensitive,
		CreatedAt: v.CreatedAt,
		UpdatedAt: v.UpdatedAt,
	}
	if !v.Sensitive {
		val := v.Value
		out.Value = &val
	}
	return out
}

// componentScope converts a path component id to the *uuid.UUID scope the
// service expects.
func componentScope(id ComponentID) *uuid.UUID {
	cid := uuid.UUID(id)
	return &cid
}

// --- App-level handlers (componentID nil) ---

func (s *Server) ListApplicationVariables(ctx context.Context, req ListApplicationVariablesRequestObject) (ListApplicationVariablesResponseObject, error) {
	out, aerr, err := s.listVariables(ctx, req.Id, nil)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListApplicationVariablesdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	return ListApplicationVariables200JSONResponse(out), nil
}

func (s *Server) CreateApplicationVariable(ctx context.Context, req CreateApplicationVariableRequestObject) (CreateApplicationVariableResponseObject, error) {
	v, aerr, err := s.createVariable(ctx, req.Id, nil, req.Body)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[CreateApplicationVariabledefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	return CreateApplicationVariable201JSONResponse(*v), nil
}

func (s *Server) UpdateApplicationVariable(ctx context.Context, req UpdateApplicationVariableRequestObject) (UpdateApplicationVariableResponseObject, error) {
	v, aerr, err := s.updateVariable(ctx, req.Id, nil, req.VariableId, req.Body)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[UpdateApplicationVariabledefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	return UpdateApplicationVariable200JSONResponse(*v), nil
}

func (s *Server) DeleteApplicationVariable(ctx context.Context, req DeleteApplicationVariableRequestObject) (DeleteApplicationVariableResponseObject, error) {
	aerr, err := s.deleteVariable(ctx, req.Id, nil, req.VariableId)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[DeleteApplicationVariabledefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	return DeleteApplicationVariable204Response{}, nil
}

// --- Component-level handlers (componentID from the path) ---

func (s *Server) ListComponentVariables(ctx context.Context, req ListComponentVariablesRequestObject) (ListComponentVariablesResponseObject, error) {
	out, aerr, err := s.listVariables(ctx, req.Id, componentScope(req.ComponentId))
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListComponentVariablesdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	return ListComponentVariables200JSONResponse(out), nil
}

func (s *Server) CreateComponentVariable(ctx context.Context, req CreateComponentVariableRequestObject) (CreateComponentVariableResponseObject, error) {
	v, aerr, err := s.createVariable(ctx, req.Id, componentScope(req.ComponentId), req.Body)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[CreateComponentVariabledefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	return CreateComponentVariable201JSONResponse(*v), nil
}

func (s *Server) UpdateComponentVariable(ctx context.Context, req UpdateComponentVariableRequestObject) (UpdateComponentVariableResponseObject, error) {
	v, aerr, err := s.updateVariable(ctx, req.Id, componentScope(req.ComponentId), req.VariableId, req.Body)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[UpdateComponentVariabledefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	return UpdateComponentVariable200JSONResponse(*v), nil
}

func (s *Server) DeleteComponentVariable(ctx context.Context, req DeleteComponentVariableRequestObject) (DeleteComponentVariableResponseObject, error) {
	aerr, err := s.deleteVariable(ctx, req.Id, componentScope(req.ComponentId), req.VariableId)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[DeleteComponentVariabledefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	return DeleteComponentVariable204Response{}, nil
}
