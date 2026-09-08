package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/policies"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// resolvePoliciesRead runs the read preamble for policy handlers: the
// service must be configured, and the caller a member.
func (s *Server) resolvePoliciesRead(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.policies == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "policies service not configured"}, nil
	}
	m, aerr, err := s.resolveMembership(ctx)
	if err != nil || aerr != nil {
		return uuid.Nil, aerr, err
	}
	return m.OrganizationID, nil, nil
}

// resolvePoliciesWrite is the read preamble plus an admin gate: a policy
// decides what every editor may apply, so it is an organization-admin
// concern.
func (s *Server) resolvePoliciesWrite(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.policies == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "policies service not configured"}, nil
	}
	m, aerr, err := s.resolveAdmin(ctx)
	if err != nil || aerr != nil {
		return uuid.Nil, aerr, err
	}
	return m.OrganizationID, nil, nil
}

func (s *Server) ListPolicies(ctx context.Context, _ ListPoliciesRequestObject) (ListPoliciesResponseObject, error) {
	orgID, aerr, err := s.resolvePoliciesRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListPoliciesdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	list, err := s.policies.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Policy, len(list))
	for i, p := range list {
		out[i] = toAPIPolicy(p)
	}
	return ListPolicies200JSONResponse(out), nil
}

func (s *Server) GetPolicy(ctx context.Context, req GetPolicyRequestObject) (GetPolicyResponseObject, error) {
	orgID, aerr, err := s.resolvePoliciesRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetPolicydefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	p, err := s.policies.Get(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[GetPolicydefaultJSONResponse](http.StatusNotFound, "not_found", "policy not found"), nil
		}
		return nil, err
	}
	return GetPolicy200JSONResponse(toAPIPolicy(p)), nil
}

func (s *Server) CreatePolicy(ctx context.Context, req CreatePolicyRequestObject) (CreatePolicyResponseObject, error) {
	orgID, aerr, err := s.resolvePoliciesWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[CreatePolicydefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[CreatePolicydefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	p := policies.CreateParams{
		Name:        strings.TrimSpace(req.Body.Name),
		Description: deref(req.Body.Description),
		Rego:        req.Body.Rego,
		Enabled:     req.Body.Enabled,
	}
	if req.Body.Enforcement != nil {
		p.Enforcement = string(*req.Body.Enforcement)
	}
	if req.Body.ApplicationId != nil {
		p.ApplicationID = *req.Body.ApplicationId
	}
	pol, err := s.policies.Create(ctx, orgID, p)
	if err != nil {
		if resp, ok := policyWriteError[CreatePolicydefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return CreatePolicy201JSONResponse(toAPIPolicy(pol)), nil
}

func (s *Server) UpdatePolicy(ctx context.Context, req UpdatePolicyRequestObject) (UpdatePolicyResponseObject, error) {
	orgID, aerr, err := s.resolvePoliciesWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[UpdatePolicydefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[UpdatePolicydefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	p := policies.UpdateParams{Name: req.Body.Name, Description: req.Body.Description, Rego: req.Body.Rego, Enabled: req.Body.Enabled}
	if req.Body.Enforcement != nil {
		e := string(*req.Body.Enforcement)
		p.Enforcement = &e
	}
	if req.Body.ApplicationId != nil {
		raw := strings.TrimSpace(*req.Body.ApplicationId)
		id := uuid.Nil
		if raw != "" {
			parsed, err := uuid.Parse(raw)
			if err != nil {
				return errResp[UpdatePolicydefaultJSONResponse](http.StatusBadRequest, "bad_request", "application_id must be a uuid or empty"), nil
			}
			id = parsed
		}
		p.ApplicationID = &id
	}
	pol, err := s.policies.Update(ctx, orgID, req.Id, p)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[UpdatePolicydefaultJSONResponse](http.StatusNotFound, "not_found", "policy not found"), nil
		}
		if resp, ok := policyWriteError[UpdatePolicydefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return UpdatePolicy200JSONResponse(toAPIPolicy(pol)), nil
}

func (s *Server) DeletePolicy(ctx context.Context, req DeletePolicyRequestObject) (DeletePolicyResponseObject, error) {
	orgID, aerr, err := s.resolvePoliciesWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[DeletePolicydefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if err := s.policies.Delete(ctx, orgID, req.Id); err != nil {
		if ent.IsNotFound(err) {
			return errResp[DeletePolicydefaultJSONResponse](http.StatusNotFound, "not_found", "policy not found"), nil
		}
		return nil, err
	}
	return DeletePolicy204Response{}, nil
}

func policyWriteError[T defaultResp](err error) (T, bool) {
	switch {
	case policies.IsValidation(err):
		return errResp[T](http.StatusBadRequest, "bad_request", err.Error()), true
	case ent.IsConstraintError(err):
		return errResp[T](http.StatusConflict, "conflict", "a policy with that name already exists in this organization"), true
	default:
		var zero T
		return zero, false
	}
}

func toAPIPolicy(p *ent.PlanPolicy) Policy {
	out := Policy{
		Id:          p.ID,
		Name:        p.Name,
		Description: optStr(p.Description),
		Rego:        p.Rego,
		Enforcement: PolicyEnforcement(p.Enforcement),
		Enabled:     p.Enabled,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	}
	if p.ApplicationID != uuid.Nil {
		id := p.ApplicationID
		out.ApplicationId = &id
	}
	return out
}

// toAPIPolicyVerdict maps a plan step's recorded verdict; nil when none.
func toAPIPolicyVerdict(cr *ent.ComponentRun) *PolicyVerdict {
	v, ok := workflows.PolicyVerdictOf(cr)
	if !ok {
		return nil
	}
	out := &PolicyVerdict{EvaluatedAt: v.EvaluatedAt, Blocked: v.Blocked, Warned: v.Warned, Results: make([]PolicyResult, 0, len(v.Results))}
	for _, r := range v.Results {
		violations := r.Violations
		if violations == nil {
			violations = []string{}
		}
		out.Results = append(out.Results, PolicyResult{
			PolicyId:    r.PolicyID,
			PolicyName:  r.PolicyName,
			Enforcement: PolicyEnforcement(r.Enforcement),
			Violations:  violations,
			Error:       optStr(r.Error),
		})
	}
	return out
}
