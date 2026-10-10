package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/tofu"
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
	managed, err := s.managedStateVersion(ctx, orgID, req.Id, req.ComponentId)
	if err != nil {
		return nil, err
	}
	cr, err := s.workflows.LatestComponentState(ctx, orgID, req.Id, req.ComponentId)
	if err != nil {
		if !ent.IsNotFound(err) {
			return nil, err
		}
		// Nothing recorded yet. A stuck lock can still exist — the very
		// first plan may have died holding it — and so can a managed state
		// (an apply that wrote state and then failed); either is worth
		// showing before any apply, so the view carries just those;
		// otherwise this is a 404 as before.
		var lock *StateLock
		if canSeeSecrets {
			if lock, err = s.componentStateLock(ctx, orgID, req.Id, req.ComponentId); err != nil {
				return nil, err
			}
		}
		if lock != nil || managed != nil {
			return GetComponentState200JSONResponse(ComponentState{Resources: []TofuResource{}, Lock: lock, ManagedState: managed}), nil
		}
		return errResp[GetComponentStatedefaultJSONResponse](http.StatusNotFound, "not_found", "no recorded state for this component"), nil
	}
	runID, crID := cr.WorkflowRunID, cr.ID
	out := ComponentState{
		RunId:          &runID,
		ComponentRunId: &crID,
		RecordedAt:     cr.FinishedAt,
		Outputs:        toAPIComponentRunOutputs(cr.Outputs, canSeeSecrets),
		Resources:      toAPITofuResources(cr.Resources),
		ManagedState:   managed,
	}
	// The latest drift check, when one has run: its verdict is parsed from the
	// step's refresh-only plan. Addresses only — the state-vs-real diffs stay on
	// the run's step detail, gated like every plan body.
	if dc, err := s.workflows.LatestDriftCheck(ctx, orgID, req.Id, req.ComponentId); err == nil {
		out.Drift = toAPIDriftStatus(dc)
	} else if !ent.IsNotFound(err) {
		return nil, err
	}
	// A stuck state lock: the component's latest settled step failed to
	// acquire it. Read from that step's logs, so gated like the logs
	// (editor-or-above) — the lock names the state's path in the backend.
	if canSeeSecrets {
		if out.Lock, err = s.componentStateLock(ctx, orgID, req.Id, req.ComponentId); err != nil {
			return nil, err
		}
	}
	return GetComponentState200JSONResponse(out), nil
}

// DownloadComponentState returns the current version of an OpenTofu
// component's managed state as the raw .tfstate file, for a member who is
// taking the resources elsewhere (their own bucket, or out of Spacefleet).
// Editor or above: state holds every secret the module touched — the same
// rule as sensitive outputs and state operations. 404 when there is nothing
// to download, including a component on a cloud backend, whose state is
// already in its own bucket.
func (s *Server) DownloadComponentState(ctx context.Context, req DownloadComponentStateRequestObject) (DownloadComponentStateResponseObject, error) {
	orgID, aerr, err := s.resolveWorkflowWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[DownloadComponentStatedefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if !s.tofuState.Enabled() {
		return errResp[DownloadComponentStatedefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "managed state is not configured on this Spacefleet"), nil
	}
	app, err := s.applications.Get(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[DownloadComponentStatedefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		}
		return nil, err
	}
	data, _, ok, err := s.tofuState.Download(ctx, orgID, req.Id, req.ComponentId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return errResp[DownloadComponentStatedefaultJSONResponse](http.StatusNotFound, "not_found", "this component has no managed state"), nil
	}
	// The component itself may be gone (state left behind by a removal);
	// its id still names the file.
	name := req.ComponentId.String()
	if c, err := s.workflows.GetComponent(ctx, orgID, req.Id, req.ComponentId); err == nil {
		name = c.Name
	} else if !ent.IsNotFound(err) {
		return nil, err
	}
	return DownloadComponentState200ApplicationoctetStreamResponse{
		Body:          bytes.NewReader(data),
		ContentLength: int64(len(data)),
		Headers: DownloadComponentState200ResponseHeaders{
			ContentDisposition: fmt.Sprintf("attachment; filename=%q", stateFilename(app.Name, name)),
			CacheControl:       "no-store",
		},
	}, nil
}

// stateFilename names a downloaded state file "<application>-<component>.tfstate".
// Both names are DNS labels already; any other character is replaced with a
// dash, so the header never needs escaping.
func stateFilename(appName, componentName string) string {
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r == '-' || r == '_' || r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				return r
			}
			return '-'
		}, s)
	}
	return clean(appName) + "-" + clean(componentName) + ".tfstate"
}

// managedStateVersion returns the current version of the component's managed
// state for the state view — metadata only — or nil when there is none (or
// managed state is not wired in).
func (s *Server) managedStateVersion(ctx context.Context, orgID, appID, componentID uuid.UUID) (*ManagedStateVersion, error) {
	v, err := s.tofuState.CurrentVersion(ctx, orgID, appID, componentID)
	if err != nil || v == nil {
		return nil, err
	}
	return &ManagedStateVersion{
		Version:   v.Version,
		Serial:    v.Serial,
		SizeBytes: v.SizeBytes,
		WrittenAt: v.CreatedAt,
		RunId:     v.WorkflowRunID,
	}, nil
}

// componentStateLock returns the lock the component's latest settled step
// could not acquire, nil when there is none (or no settled step at all).
func (s *Server) componentStateLock(ctx context.Context, orgID, appID, componentID uuid.UUID) (*StateLock, error) {
	step, err := s.workflows.LatestSettledStep(ctx, orgID, appID, componentID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return toAPIStateLock(step), nil
}

// toAPIStateLock maps a settled step to the lock it could not acquire — nil
// for a step that succeeded (any lock is released) or that failed for another
// reason.
func toAPIStateLock(cr *ent.ComponentRun) *StateLock {
	if cr.Status != "failed" {
		return nil
	}
	li := tofu.ParseLockInfo(cr.Logs)
	if li == nil {
		return nil
	}
	return &StateLock{
		RunId:          cr.WorkflowRunID,
		ComponentRunId: cr.ID,
		FailedAt:       cr.FinishedAt,
		Id:             li.ID,
		Path:           optStr(li.Path),
		Operation:      optStr(li.Operation),
		Who:            optStr(li.Who),
		Version:        optStr(li.Version),
		Created:        optStr(li.Created),
		Info:           optStr(li.Info),
	}
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
