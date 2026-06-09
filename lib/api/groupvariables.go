package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/lib/variables"
)

// groupVariablesPreamble runs the common preamble for every group-variable
// handler: confirm the variables + application-groups services exist, resolve +
// authorize membership (editor-or-above for writes), and confirm the group
// belongs to the org (so a variable can't be hung off another org's group).
// Returns the org id. Mirrors variablesPreamble.
func (s *Server) groupVariablesPreamble(ctx context.Context, groupID uuid.UUID, write bool) (uuid.UUID, *apiError, error) {
	if s.variables == nil || s.applicationGroups == nil {
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
	if _, err := s.applicationGroups.Get(ctx, orgID, groupID); err != nil {
		if ent.IsNotFound(err) {
			return uuid.Nil, &apiError{http.StatusNotFound, "not_found", "application group not found"}, nil
		}
		return uuid.Nil, nil, err
	}
	return orgID, nil, nil
}

// toAPIGroupVariable maps a group-variable ent row to the API type. Like
// toAPIVariable it never exposes the sealed value: a sensitive variable returns
// only its name + the sensitive flag; a non-secret one also returns its value.
func toAPIGroupVariable(v *ent.GroupVariable) Variable {
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

func (s *Server) ListGroupVariables(ctx context.Context, req ListGroupVariablesRequestObject) (ListGroupVariablesResponseObject, error) {
	orgID, aerr, err := s.groupVariablesPreamble(ctx, req.Id, false)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListGroupVariablesdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	rows, err := s.variables.ListGroup(ctx, orgID, req.Id)
	if err != nil {
		return nil, err
	}
	out := make([]Variable, len(rows))
	for i, v := range rows {
		out[i] = toAPIGroupVariable(v)
	}
	return ListGroupVariables200JSONResponse(out), nil
}

func (s *Server) CreateGroupVariable(ctx context.Context, req CreateGroupVariableRequestObject) (CreateGroupVariableResponseObject, error) {
	orgID, aerr, err := s.groupVariablesPreamble(ctx, req.Id, true)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[CreateGroupVariabledefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[CreateGroupVariabledefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	v, err := s.variables.CreateGroup(ctx, orgID, req.Id, variables.CreateParams{
		Name:      strings.TrimSpace(req.Body.Name),
		Sensitive: req.Body.Sensitive,
		Value:     req.Body.Value,
	})
	if err != nil {
		aerr, ierr := variableWriteAPIError(err)
		if ierr != nil {
			return nil, ierr
		}
		return errResp[CreateGroupVariabledefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	return CreateGroupVariable201JSONResponse(toAPIGroupVariable(v)), nil
}

func (s *Server) UpdateGroupVariable(ctx context.Context, req UpdateGroupVariableRequestObject) (UpdateGroupVariableResponseObject, error) {
	orgID, aerr, err := s.groupVariablesPreamble(ctx, req.Id, true)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[UpdateGroupVariabledefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[UpdateGroupVariabledefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	params := variables.UpdateParams{Value: req.Body.Value}
	if req.Body.Name != nil {
		name := strings.TrimSpace(*req.Body.Name)
		params.Name = &name
	}
	v, err := s.variables.UpdateGroup(ctx, orgID, req.Id, req.VariableId, params)
	if err != nil {
		aerr, ierr := variableWriteAPIError(err)
		if ierr != nil {
			return nil, ierr
		}
		return errResp[UpdateGroupVariabledefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	return UpdateGroupVariable200JSONResponse(toAPIGroupVariable(v)), nil
}

func (s *Server) DeleteGroupVariable(ctx context.Context, req DeleteGroupVariableRequestObject) (DeleteGroupVariableResponseObject, error) {
	orgID, aerr, err := s.groupVariablesPreamble(ctx, req.Id, true)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[DeleteGroupVariabledefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if err := s.variables.DeleteGroup(ctx, orgID, req.Id, req.VariableId); err != nil {
		if ent.IsNotFound(err) {
			return errResp[DeleteGroupVariabledefaultJSONResponse](http.StatusNotFound, "not_found", "variable not found"), nil
		}
		return nil, err
	}
	return DeleteGroupVariable204Response{}, nil
}
