package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/policies"
	"github.com/spacefleet/spacefleet/lib/policy"
	"github.com/spacefleet/spacefleet/lib/tofu"
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

// ListPolicyTestPlans lists recent OpenTofu plan steps an admin can dry-run
// a policy against. Admin only: the list carries plan headlines (counts),
// which sit behind the editor gate elsewhere, and policy authoring is an
// admin concern anyway.
func (s *Server) ListPolicyTestPlans(ctx context.Context, _ ListPolicyTestPlansRequestObject) (ListPolicyTestPlansResponseObject, error) {
	orgID, aerr, err := s.resolvePoliciesWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListPolicyTestPlansdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if s.workflows == nil {
		return errResp[ListPolicyTestPlansdefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "workflows service not configured"), nil
	}
	steps, err := s.workflows.RecentPlanSteps(ctx, orgID, policyTestPlanLimit)
	if err != nil {
		return nil, err
	}
	out := make([]PolicyTestPlan, 0, len(steps))
	for _, ps := range steps {
		p := tofu.ParsePlan(ps.Step.Logs)
		out = append(out, PolicyTestPlan{
			ComponentRunId:  ps.Step.ID,
			RunId:           ps.Run.ID,
			ApplicationId:   ps.App.ID,
			ApplicationName: ps.App.Name,
			ComponentName:   ps.ComponentName(),
			Action:          RunAction(ps.Run.Action),
			FinishedAt:      ps.Step.FinishedAt,
			Add:             p.Add,
			Change:          p.Change,
			Destroy:         p.Destroy,
			Replace:         p.Replace,
		})
	}
	return ListPolicyTestPlans200JSONResponse(out), nil
}

// policyTestPlanLimit bounds the dry-run plan picker: recent plans are the
// useful ones, and each row parses a plan from its logs.
const policyTestPlanLimit = 25

// TestPolicy evaluates a Rego policy — as typed, not yet saved — against a
// past plan step, exactly as the gate would have: it answers "what would
// this policy have said about that plan" without enabling anything. Admin
// only. The Rego must compile (400 with the compiler's message, as on
// create); the step must be one of the organization's succeeded OpenTofu
// plan steps (404 otherwise).
func (s *Server) TestPolicy(ctx context.Context, req TestPolicyRequestObject) (TestPolicyResponseObject, error) {
	orgID, aerr, err := s.resolvePoliciesWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[TestPolicydefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if s.workflows == nil {
		return errResp[TestPolicydefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "workflows service not configured"), nil
	}
	if req.Body == nil {
		return errResp[TestPolicydefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	rego := req.Body.Rego
	if strings.TrimSpace(rego) == "" {
		return errResp[TestPolicydefaultJSONResponse](http.StatusBadRequest, "bad_request", "rego is required"), nil
	}
	if err := policy.Compile(rego); err != nil {
		return errResp[TestPolicydefaultJSONResponse](http.StatusBadRequest, "bad_request", err.Error()), nil
	}
	ps, err := s.workflows.PlanStep(ctx, orgID, req.Body.ComponentRunId)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[TestPolicydefaultJSONResponse](http.StatusNotFound, "not_found", "no such OpenTofu plan step in this organization"), nil
		}
		return nil, err
	}
	in := workflows.PolicyInputFor(ps)
	verdict := policy.Evaluate(ctx, []policy.Policy{{ID: "test", Name: "test", Rego: rego, Enforcement: policy.EnforcementBlock}}, in)
	r := verdict.Results[0]
	violations := r.Violations
	if violations == nil {
		violations = []string{}
	}
	// The input the policy saw, as a plain object: the same document the
	// docs describe, so an author can see what a rule matched against.
	var input map[string]interface{}
	if raw, err := json.Marshal(in); err == nil {
		_ = json.Unmarshal(raw, &input)
	}
	return TestPolicy200JSONResponse(PolicyTestResult{Violations: violations, Error: optStr(r.Error), Input: input}), nil
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
