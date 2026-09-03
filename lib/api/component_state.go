package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/spacefleet/spacefleet/ent"
)

// GetComponentState returns an OpenTofu component's last recorded state — the
// outputs and managed-resource inventory its most recent successful apply
// captured — so a member can see what the component owns without opening run
// history. Read access (viewer or above); sensitive output values are omitted
// below editor exactly as on a component run. A component that has never
// applied successfully, is not an OpenTofu component, or is outside the
// caller's org is a 404.
func (s *Server) GetComponentState(ctx context.Context, req GetComponentStateRequestObject) (GetComponentStateResponseObject, error) {
	orgID, canSeeSecrets, aerr, err := s.resolveWorkflowRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetComponentStatedefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	cr, err := s.workflows.LatestComponentState(ctx, orgID, req.Id, req.ComponentId)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[GetComponentStatedefaultJSONResponse](http.StatusNotFound, "not_found", "no recorded state for this component"), nil
		}
		return nil, err
	}
	out := ComponentState{
		RunId:          cr.WorkflowRunID,
		ComponentRunId: cr.ID,
		RecordedAt:     cr.FinishedAt,
		Outputs:        toAPIComponentRunOutputs(cr.Outputs, canSeeSecrets),
		Resources:      toAPITofuResources(cr.Resources),
	}
	// The latest drift check, when one has run: its verdict is parsed from the
	// step's refresh-only plan. Addresses only — the state-vs-real diffs stay on
	// the run's step detail, gated like every plan body.
	if dc, err := s.workflows.LatestDriftCheck(ctx, orgID, req.Id, req.ComponentId); err == nil {
		out.Drift = toAPIDriftStatus(dc)
	} else if !ent.IsNotFound(err) {
		return nil, err
	}
	return GetComponentState200JSONResponse(out), nil
}

// toAPIDriftStatus maps a settled drift-check step to the API drift status.
// A failed check (the plan itself errored) reports has_drift=false with its
// failed status, so a client can show "check failed" rather than "clean".
func toAPIDriftStatus(cr *ent.ComponentRun) *DriftStatus {
	out := &DriftStatus{
		RunId:          cr.WorkflowRunID,
		ComponentRunId: cr.ID,
		CheckedAt:      cr.FinishedAt,
		Status:         ComponentRunStatus(cr.Status),
	}
	if p := parseTofuPlan(cr); p != nil && cr.Status == "succeeded" {
		out.HasDrift = p.HasDrift
		if len(p.Drift) > 0 {
			summary := toAPIPlanSummary(*p, false)
			out.Drift = summary.Drift
		}
	}
	return out
}

// toAPITofuResources maps the stored inventory JSON (component_runs.resources,
// the array the apply step reduced from the state) to the API list. There is
// nothing to redact — the inventory carries identity fields only, never
// attribute values. An empty or unparseable column yields an empty list.
func toAPITofuResources(raw string) []TofuResource {
	out := []TofuResource{}
	if raw == "" {
		return out
	}
	var stored []struct {
		Address  string          `json:"address"`
		Mode     string          `json:"mode"`
		Type     string          `json:"type"`
		Name     string          `json:"name"`
		Provider string          `json:"provider"`
		ID       json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return out
	}
	for _, r := range stored {
		mapped := TofuResource{
			Address:  r.Address,
			Mode:     r.Mode,
			Type:     r.Type,
			Name:     r.Name,
			Provider: optStr(r.Provider),
		}
		if len(r.ID) > 0 {
			var v interface{}
			if err := json.Unmarshal(r.ID, &v); err == nil && v != nil {
				mapped.Id = v
			}
		}
		out = append(out, mapped)
	}
	return out
}
