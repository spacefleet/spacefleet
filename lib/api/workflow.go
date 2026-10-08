package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// resolveWorkflowRead is the read preamble for workflow/run handlers: confirm
// the workflows service exists, then resolve + authorize org membership, also
// returning whether the caller may see secret-bearing component config (editor
// or above) — mirrors resolveAppRead. The applications service is needed too (a
// workflow hangs off an application), so a nil applications service is a 503.
func (s *Server) resolveWorkflowRead(ctx context.Context) (uuid.UUID, bool, *apiError, error) {
	if s.workflows == nil {
		return uuid.Nil, false, &apiError{http.StatusServiceUnavailable, "unavailable", "workflows service not configured"}, nil
	}
	return s.resolveAppRead(ctx)
}

// resolveWorkflowWrite is resolveWorkflowRead plus an editor-or-above gate, for
// the handlers that change state (replace the workflow, start a run).
func (s *Server) resolveWorkflowWrite(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.workflows == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "workflows service not configured"}, nil
	}
	return s.resolveAppWrite(ctx)
}

// GetApplicationWorkflow returns the application's workflow: its stages in run
// order, each with its components. Read access (viewer or above);
// secret-bearing config is redacted below editor.
func (s *Server) GetApplicationWorkflow(ctx context.Context, req GetApplicationWorkflowRequestObject) (GetApplicationWorkflowResponseObject, error) {
	orgID, canSeeSecrets, aerr, err := s.resolveWorkflowRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetApplicationWorkflowdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	stages, err := s.workflows.GetWorkflow(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[GetApplicationWorkflowdefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		}
		return nil, err
	}
	return GetApplicationWorkflow200JSONResponse(toAPIWorkflow(stages, canSeeSecrets)), nil
}

// ReplaceApplicationWorkflow validates the proposed workflow and atomically
// replaces the application's stages and components with it. Editor or above; a
// validation failure is a 400.
func (s *Server) ReplaceApplicationWorkflow(ctx context.Context, req ReplaceApplicationWorkflowRequestObject) (ReplaceApplicationWorkflowResponseObject, error) {
	orgID, aerr, err := s.resolveWorkflowWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ReplaceApplicationWorkflowdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[ReplaceApplicationWorkflowdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	stages := make([]workflows.StageInput, len(req.Body.Stages))
	for i, st := range req.Body.Stages {
		stages[i] = toStageInput(st)
	}
	opts := workflows.ReplaceOptions{AllowBackendChange: req.Body.AllowBackendChange != nil && *req.Body.AllowBackendChange}
	saved, err := s.workflows.ReplaceWorkflowWith(ctx, orgID, req.Id, stages, opts)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[ReplaceApplicationWorkflowdefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		}
		if errors.Is(err, workflows.ErrBackendChange) {
			return errResp[ReplaceApplicationWorkflowdefaultJSONResponse](http.StatusConflict, "backend_change", err.Error()), nil
		}
		if isWorkflowValidation(err) {
			return errResp[ReplaceApplicationWorkflowdefaultJSONResponse](http.StatusBadRequest, "bad_request", err.Error()), nil
		}
		return nil, err
	}
	// The writer is editor-or-above, so the round-tripped components are returned
	// unredacted (canSee=true), keeping the builder's edit form populated.
	return ReplaceApplicationWorkflow200JSONResponse(toAPIWorkflow(saved, true)), nil
}

// isWorkflowValidation reports whether err is one of the workflow/config
// validation sentinels (or the invalid-action sentinel) the service returns,
// which a handler maps to 400.
func isWorkflowValidation(err error) bool {
	return errors.Is(err, workflows.ErrDuplicateID) ||
		errors.Is(err, workflows.ErrMissingID) ||
		errors.Is(err, workflows.ErrInvalidStage) ||
		errors.Is(err, workflows.ErrInvalidConfig) ||
		errors.Is(err, workflows.ErrInvalidTarget) ||
		errors.Is(err, workflows.ErrInvalidAction)
}

// secretConfigKeys are the component config keys that may carry secrets and so
// are withheld from callers below editor. These are not sealed at rest,
// mirroring redactAppSecrets on the application:
//   - "values": the raw Helm inline values override.
//   - "backend_config": the OpenTofu backend override, which can carry S3 access
//     keys, azurerm secrets, a pg backend conn_str password, etc.
var secretConfigKeys = []string{"values", "backend_config"}

// toStageInput maps an API WorkflowStageInput to the service input, keeping the
// order of its components.
func toStageInput(st WorkflowStageInput) workflows.StageInput {
	in := workflows.StageInput{
		ID:         st.Id,
		Name:       strings.TrimSpace(st.Name),
		Components: make([]workflows.ComponentInput, len(st.Components)),
	}
	for i, c := range st.Components {
		in.Components[i] = toComponentInput(c)
	}
	return in
}

// toComponentInput maps an API ComponentInput to the service input. Optional
// fields default to their zero value; the builder sends a stable
// client-provided id so the component's variables and history survive the
// replace.
func toComponentInput(c ComponentInput) workflows.ComponentInput {
	in := workflows.ComponentInput{
		ID:                   c.Id,
		Name:                 strings.TrimSpace(c.Name),
		Type:                 string(c.Type),
		Config:               derefMap(c.Config),
		ContinueOnFailure:    c.ContinueOnFailure != nil && *c.ContinueOnFailure,
		RequiresApproval:     c.RequiresApproval != nil && *c.RequiresApproval,
		ApprovalPolicy:       toApprovalPolicy(c.ApprovalPolicy),
		TargetClusterID:      c.TargetClusterId,
		TargetNamespace:      strings.TrimSpace(deref(c.TargetNamespace)),
		ChartCredentialID:    c.ChartCredentialId,
		GitHubInstallationID: c.GithubInstallationId,
	}
	return in
}

// toAPIWorkflow maps the application's stages (with their components eager
// loaded) to the API workflow, redacting secret-bearing config for callers
// below editor (canSee=false).
func toAPIWorkflow(stages []*ent.WorkflowStage, canSee bool) Workflow {
	out := Workflow{Stages: make([]WorkflowStage, len(stages))}
	for i, st := range stages {
		comps := make([]Component, len(st.Edges.Components))
		for j, c := range st.Edges.Components {
			comps[j] = toAPIComponent(c, canSee)
		}
		out.Stages[i] = WorkflowStage{Id: st.ID, Name: st.Name, Components: comps}
	}
	return out
}

// toAPIComponent maps one component row to the API type. The config map is
// copied with secret keys stripped when canSee is false, so a viewer never
// receives the inline `values`.
func toAPIComponent(c *ent.Component, canSee bool) Component {
	out := Component{
		Id:                c.ID,
		Name:              c.Name,
		Type:              ComponentType(c.Type),
		Config:            redactConfig(c.Config, canSee),
		ContinueOnFailure: c.ContinueOnFailure,
		RequiresApproval:  &c.RequiresApproval,
		ApprovalPolicy:    toAPIApprovalPolicy(c.ApprovalPolicy),
	}
	if c.TargetNamespace != "" {
		ns := c.TargetNamespace
		out.TargetNamespace = &ns
	}
	if c.TargetClusterID != uuid.Nil {
		id := c.TargetClusterID
		out.TargetClusterId = &id
	}
	if c.ChartCredentialID != uuid.Nil {
		id := c.ChartCredentialID
		out.ChartCredentialId = &id
	}
	if c.GithubInstallationID != uuid.Nil {
		id := c.GithubInstallationID
		out.GithubInstallationId = &id
	}
	return out
}

// redactConfig returns a copy of the config map with secret-bearing keys removed
// when the caller may not see them (canSee=false). It always returns a non-nil
// map so the required `config` field is present.
func redactConfig(in map[string]string, canSee bool) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	if !canSee {
		for _, k := range secretConfigKeys {
			delete(out, k)
		}
	}
	return out
}

// derefMap returns the pointed-to map, or nil.
func derefMap(p *map[string]string) map[string]string {
	if p == nil {
		return nil
	}
	return *p
}

// toApprovalPolicy maps the API policy (all fields optional) to the service
// policy; a nil object is the default policy. The service normalises it.
func toApprovalPolicy(p *ApprovalPolicy) workflows.ApprovalPolicy {
	if p == nil {
		return workflows.ApprovalPolicy{}
	}
	out := workflows.ApprovalPolicy{}
	if p.Approvers != nil {
		out.Approvers = *p.Approvers
	}
	if p.Required != nil {
		out.Required = *p.Required
	}
	if p.RequireDifferentApprover != nil {
		out.RequireDifferentApprover = *p.RequireDifferentApprover
	}
	if p.TimeoutMinutes != nil {
		out.TimeoutMinutes = *p.TimeoutMinutes
	}
	return out
}

// toAPIApprovalPolicy maps a stored policy to the API shape, omitting the
// object entirely for the default policy so the common component reads
// unchanged.
func toAPIApprovalPolicy(p workflows.ApprovalPolicy) *ApprovalPolicy {
	if len(p.Approvers) == 0 && p.Required == 0 && !p.RequireDifferentApprover && p.TimeoutMinutes == 0 {
		return nil
	}
	out := &ApprovalPolicy{}
	if len(p.Approvers) > 0 {
		approvers := append([]string(nil), p.Approvers...)
		out.Approvers = &approvers
	}
	if p.Required != 0 {
		r := p.Required
		out.Required = &r
	}
	if p.RequireDifferentApprover {
		t := true
		out.RequireDifferentApprover = &t
	}
	if p.TimeoutMinutes != 0 {
		m := p.TimeoutMinutes
		out.TimeoutMinutes = &m
	}
	return out
}
