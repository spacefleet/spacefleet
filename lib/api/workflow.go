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

// GetApplicationWorkflow returns the application's workflow components. Read
// access (viewer or above); secret-bearing config is redacted below editor.
func (s *Server) GetApplicationWorkflow(ctx context.Context, req GetApplicationWorkflowRequestObject) (GetApplicationWorkflowResponseObject, error) {
	orgID, canSeeSecrets, aerr, err := s.resolveWorkflowRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetApplicationWorkflowdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	comps, err := s.workflows.ListComponents(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[GetApplicationWorkflowdefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		}
		return nil, err
	}
	return GetApplicationWorkflow200JSONResponse(toAPIWorkflow(comps, canSeeSecrets)), nil
}

// ReplaceApplicationWorkflow validates the proposed DAG and atomically replaces
// the application's components with it. Editor or above; a DAG/config validation
// failure is a 400.
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
	nodes := make([]workflows.ComponentInput, len(req.Body.Components))
	for i, c := range req.Body.Components {
		nodes[i] = toComponentInput(c)
	}
	comps, err := s.workflows.ReplaceWorkflow(ctx, orgID, req.Id, nodes)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[ReplaceApplicationWorkflowdefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		}
		if isWorkflowValidation(err) {
			return errResp[ReplaceApplicationWorkflowdefaultJSONResponse](http.StatusBadRequest, "bad_request", err.Error()), nil
		}
		return nil, err
	}
	// The writer is editor-or-above, so the round-tripped components are returned
	// unredacted (canSee=true), keeping the canvas edit form populated.
	return ReplaceApplicationWorkflow200JSONResponse(toAPIWorkflow(comps, true)), nil
}

// isWorkflowValidation reports whether err is one of the DAG/config validation
// sentinels (or the invalid-action sentinel) the service returns, which a
// handler maps to 400.
func isWorkflowValidation(err error) bool {
	return errors.Is(err, workflows.ErrDuplicateID) ||
		errors.Is(err, workflows.ErrMissingID) ||
		errors.Is(err, workflows.ErrUnknownDependency) ||
		errors.Is(err, workflows.ErrSelfDependency) ||
		errors.Is(err, workflows.ErrCycle) ||
		errors.Is(err, workflows.ErrInvalidConfig) ||
		errors.Is(err, workflows.ErrInvalidAction)
}

// secretConfigKeys are the component config keys that may carry secrets and so
// are withheld from callers below editor. Today that's the raw Helm `values`
// override (not sealed at rest), mirroring redactAppSecrets on the application.
var secretConfigKeys = []string{"values"}

// toComponentInput maps an API ComponentInput to the service input. Optional
// fields default to their zero value; the canvas sends a stable client-provided
// id so depends_on edges survive the replace.
func toComponentInput(c ComponentInput) workflows.ComponentInput {
	in := workflows.ComponentInput{
		ID:                   c.Id,
		Name:                 strings.TrimSpace(c.Name),
		Type:                 string(c.Type),
		Config:               derefMap(c.Config),
		ContinueOnFailure:    c.ContinueOnFailure != nil && *c.ContinueOnFailure,
		TargetClusterID:      c.TargetClusterId,
		TargetNamespace:      strings.TrimSpace(deref(c.TargetNamespace)),
		ChartCredentialID:    c.ChartCredentialId,
		GitHubInstallationID: c.GithubInstallationId,
		Position:             float32MapToFloat64(c.Position),
	}
	if c.DependsOn != nil {
		in.DependsOn = *c.DependsOn
	}
	return in
}

// toAPIWorkflow maps the application's component rows to the API workflow,
// redacting secret-bearing config for callers below editor (canSee=false).
func toAPIWorkflow(comps []*ent.Component, canSee bool) Workflow {
	out := Workflow{Components: make([]Component, len(comps))}
	for i, c := range comps {
		out.Components[i] = toAPIComponent(c, canSee)
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
		DependsOn:         nonNilUUIDs(c.DependsOn),
		ContinueOnFailure: c.ContinueOnFailure,
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
	if len(c.Position) > 0 {
		pos := float64MapToFloat32(c.Position)
		out.Position = &pos
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

// nonNilUUIDs returns the slice or an empty (non-nil) slice, so the required
// `depends_on` array serializes as [] rather than null.
func nonNilUUIDs(in []uuid.UUID) []uuid.UUID {
	if in == nil {
		return []uuid.UUID{}
	}
	return in
}

// float32MapToFloat64 converts the API position map (float32) to the storage
// shape (float64). Nil stays nil.
func float32MapToFloat64(in *map[string]float32) map[string]float64 {
	if in == nil {
		return nil
	}
	out := make(map[string]float64, len(*in))
	for k, v := range *in {
		out[k] = float64(v)
	}
	return out
}

// float64MapToFloat32 converts the stored position map (float64) to the API
// shape (float32).
func float64MapToFloat32(in map[string]float64) map[string]float32 {
	out := make(map[string]float32, len(in))
	for k, v := range in {
		out[k] = float32(v)
	}
	return out
}
