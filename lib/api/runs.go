package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// ListRuns returns an application's workflow runs (newest first). Read access
// (viewer or above).
func (s *Server) ListRuns(ctx context.Context, req ListRunsRequestObject) (ListRunsResponseObject, error) {
	orgID, _, aerr, err := s.resolveWorkflowRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListRunsdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	list, err := s.workflows.ListRuns(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[ListRunsdefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		}
		return nil, err
	}
	out := make([]WorkflowRun, len(list))
	for i, r := range list {
		out[i] = toAPIWorkflowRun(r)
	}
	return ListRuns200JSONResponse(RunList{Runs: out}), nil
}

// StartRun snapshots the current workflow, opens a run, enqueues the executor
// job, and records its id. Editor or above; needs the background worker (503
// otherwise). A run already in flight is a 409; an invalid action is a 400.
func (s *Server) StartRun(ctx context.Context, req StartRunRequestObject) (StartRunResponseObject, error) {
	orgID, aerr, err := s.resolveWorkflowWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[StartRundefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[StartRundefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	if s.jobQueue == nil {
		return errResp[StartRundefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "background job worker not configured; cannot start a run"), nil
	}
	action := string(req.Body.Action)
	run, err := s.workflows.BeginRun(ctx, orgID, req.Id, action)
	if err != nil {
		switch {
		case ent.IsNotFound(err):
			return errResp[StartRundefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		case errors.Is(err, workflows.ErrRunInFlight):
			return errResp[StartRundefaultJSONResponse](http.StatusConflict, "conflict", err.Error()), nil
		case errors.Is(err, workflows.ErrInvalidAction):
			return errResp[StartRundefaultJSONResponse](http.StatusBadRequest, "bad_request", "action must be deploy, uninstall, or preview"), nil
		default:
			return nil, err
		}
	}
	// force is the per-run forced-roll opt-in; only meaningful for deploy (the
	// planner ignores it for uninstall/preview) but carried on the job args so a
	// River retry re-plans identically. Defaults to false when absent.
	force := req.Body.Force != nil && *req.Body.Force
	// Enqueueing is non-atomic by design (the queue isn't part of the ent tx),
	// mirroring beginRollout: BeginRun commits the pending run, then we enqueue and
	// record the job id. The pending run is what arms the application's in-flight
	// gate, so if the Insert fails we must not leave the run pending — that would
	// wedge the gate and refuse every future run. Mark the run failed before
	// returning so the gate clears; the caller can retry.
	res, err := s.jobQueue.Insert(ctx, workflows.WorkflowRunArgs{
		WorkflowRunID: run.ID,
		OrgID:         orgID,
		ApplicationID: req.Id,
		Action:        action,
		Force:         force,
	})
	if err != nil {
		_ = s.workflows.MarkRun(ctx, orgID, run.ID, "failed", "failed to enqueue run: "+err.Error())
		return nil, err
	}
	jobID := strconv.FormatInt(res.Job.ID, 10)
	if err := s.workflows.SetRunJob(ctx, orgID, run.ID, jobID); err != nil {
		return nil, err
	}
	run.JobID = jobID
	return StartRun202JSONResponse(toAPIWorkflowRun(run)), nil
}

// CancelRun marks an in-flight run failed and settles its non-terminal component
// runs, clearing the application's in-flight gate. Editor or above. A run already
// terminal is a 409 (nothing to cancel); a run not in the org/app is a 404.
func (s *Server) CancelRun(ctx context.Context, req CancelRunRequestObject) (CancelRunResponseObject, error) {
	orgID, aerr, err := s.resolveWorkflowWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[CancelRundefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	run, err := s.workflows.CancelRun(ctx, orgID, req.Id, req.RunId)
	if err != nil {
		switch {
		case ent.IsNotFound(err):
			return errResp[CancelRundefaultJSONResponse](http.StatusNotFound, "not_found", "run not found"), nil
		case errors.Is(err, workflows.ErrRunNotInFlight):
			return errResp[CancelRundefaultJSONResponse](http.StatusConflict, "conflict", err.Error()), nil
		default:
			return nil, err
		}
	}
	// CancelRun made the cancel durable in the DB; now stop the run's River job so a
	// worker actively executing the DAG halts (and won't retry) rather than running
	// to completion and racing the cancelled status. Best-effort: a missing/finished
	// job is a no-op, and a pending run with no job id simply has nothing to cancel.
	if s.jobQueue != nil && run.JobID != "" {
		if jobID, perr := strconv.ParseInt(run.JobID, 10, 64); perr == nil {
			_ = s.jobQueue.JobCancel(ctx, jobID)
		}
	}
	return CancelRun200JSONResponse(toAPIWorkflowRun(run)), nil
}

// ApproveComponentRun approves a step parked at a manual-approval gate, then
// resumes the run. Editor or above; needs the background worker (503 otherwise).
// A step not awaiting approval is a 409.
func (s *Server) ApproveComponentRun(ctx context.Context, req ApproveComponentRunRequestObject) (ApproveComponentRunResponseObject, error) {
	run, aerr, err := s.decideComponentRun(ctx, req.Id, req.RunId, req.ComponentRunId, workflows.DecisionApprove)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ApproveComponentRundefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	return ApproveComponentRun200JSONResponse(toAPIWorkflowRun(run)), nil
}

// RejectComponentRun rejects a step parked at a manual-approval gate (the step
// settles failed), then resumes the run so it settles. Editor or above; needs
// the background worker (503 otherwise). A step not awaiting approval is a 409.
func (s *Server) RejectComponentRun(ctx context.Context, req RejectComponentRunRequestObject) (RejectComponentRunResponseObject, error) {
	run, aerr, err := s.decideComponentRun(ctx, req.Id, req.RunId, req.ComponentRunId, workflows.DecisionReject)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[RejectComponentRundefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	return RejectComponentRun200JSONResponse(toAPIWorkflowRun(run)), nil
}

// decideComponentRun is the shared approve/reject body: resolve + authorize
// (editor or above), require the background worker, record the decision against
// the authenticated user, then enqueue a resume job and record its id. Both
// decisions enqueue a resume — approve so the gated node runs, reject so the run
// settles (the rejected step is already failed, its dependents skip on resume).
func (s *Server) decideComponentRun(ctx context.Context, appID, runID, crID uuid.UUID, decision string) (*ent.WorkflowRun, *apiError, error) {
	orgID, aerr, err := s.resolveWorkflowWrite(ctx)
	if err != nil {
		return nil, nil, err
	}
	if aerr != nil {
		return nil, aerr, nil
	}
	if s.jobQueue == nil {
		return nil, &apiError{http.StatusServiceUnavailable, "unavailable", "background job worker not configured; cannot resume a run"}, nil
	}
	// The approver/rejector identity is the authenticated user; prefer their
	// email (what an operator recognizes) and fall back to the id.
	u, err := s.currentUser(ctx)
	if err != nil {
		return nil, nil, err
	}
	approver := u.Email
	if approver == "" {
		approver = u.ID.String()
	}

	run, err := s.workflows.ApproveComponentRun(ctx, orgID, appID, runID, crID, approver, decision)
	if err != nil {
		switch {
		case ent.IsNotFound(err):
			return nil, &apiError{http.StatusNotFound, "not_found", "component run not found"}, nil
		case errors.Is(err, workflows.ErrNotAwaitingApproval):
			return nil, &apiError{http.StatusConflict, "conflict", err.Error()}, nil
		case errors.Is(err, workflows.ErrInvalidDecision):
			return nil, &apiError{http.StatusBadRequest, "bad_request", err.Error()}, nil
		default:
			return nil, nil, err
		}
	}

	// Enqueue a fresh executor job to resume the run. The run is still
	// awaiting_approval in the DB (ApproveComponentRun left it so); the resumed
	// worker flips it back to running and re-drives the DAG — short-circuiting
	// already-terminal nodes and re-evaluating the now-cleared gate. Mirrors
	// StartRun: enqueue with the same ids/action, then record the new job id.
	res, err := s.jobQueue.Insert(ctx, workflows.WorkflowRunArgs{
		WorkflowRunID: run.ID,
		OrgID:         orgID,
		ApplicationID: appID,
		Action:        string(run.Action),
	})
	if err != nil {
		return nil, nil, err
	}
	jobID := strconv.FormatInt(res.Job.ID, 10)
	if err := s.workflows.SetRunJob(ctx, orgID, run.ID, jobID); err != nil {
		return nil, nil, err
	}
	run.JobID = jobID
	return run, nil, nil
}

// GetRun returns one workflow run with its component runs and graph snapshot.
// Read access (viewer or above); secret-bearing config in the snapshot is
// redacted below editor.
func (s *Server) GetRun(ctx context.Context, req GetRunRequestObject) (GetRunResponseObject, error) {
	orgID, canSeeSecrets, aerr, err := s.resolveWorkflowRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetRundefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	run, steps, err := s.workflows.GetRun(ctx, orgID, req.Id, req.RunId)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[GetRundefaultJSONResponse](http.StatusNotFound, "not_found", "run not found"), nil
		}
		return nil, err
	}
	return GetRun200JSONResponse(toAPIWorkflowRunDetail(run, steps, canSeeSecrets)), nil
}

// GetComponentRun returns one component run within a run, with its logs. Read
// access (viewer or above).
func (s *Server) GetComponentRun(ctx context.Context, req GetComponentRunRequestObject) (GetComponentRunResponseObject, error) {
	orgID, canSeeSecrets, aerr, err := s.resolveWorkflowRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetComponentRundefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	cr, err := s.workflows.GetComponentRun(ctx, orgID, req.Id, req.RunId, req.ComponentRunId)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[GetComponentRundefaultJSONResponse](http.StatusNotFound, "not_found", "component run not found"), nil
		}
		return nil, err
	}
	return GetComponentRun200JSONResponse(toAPIComponentRunDetail(cr, canSeeSecrets)), nil
}

// toAPIWorkflowRun maps a run row to the API list/summary type. started_at /
// finished_at pass through as nullable times.
func toAPIWorkflowRun(r *ent.WorkflowRun) WorkflowRun {
	return WorkflowRun{
		Id:            r.ID,
		ApplicationId: r.ApplicationID,
		Action:        RunAction(r.Action),
		Status:        RunStatus(r.Status),
		Message:       optStr(r.Message),
		CreatedAt:     r.CreatedAt,
		StartedAt:     r.StartedAt,
		FinishedAt:    r.FinishedAt,
	}
}

// toAPIWorkflowRunDetail is toAPIWorkflowRun plus the component runs and the
// graph snapshot, redacting secret-bearing config in the snapshot for callers
// below editor.
func toAPIWorkflowRunDetail(r *ent.WorkflowRun, steps []*ent.ComponentRun, canSee bool) WorkflowRunDetail {
	b := toAPIWorkflowRun(r)
	out := WorkflowRunDetail{
		Id:            b.Id,
		ApplicationId: b.ApplicationId,
		Action:        b.Action,
		Status:        b.Status,
		Message:       b.Message,
		CreatedAt:     b.CreatedAt,
		StartedAt:     b.StartedAt,
		FinishedAt:    b.FinishedAt,
		ComponentRuns: make([]ComponentRun, len(steps)),
	}
	for i, cr := range steps {
		out.ComponentRuns[i] = toAPIComponentRun(cr)
	}
	if graph := redactGraph(r.Graph, canSee); graph != "" {
		out.Graph = &graph
	}
	return out
}

// toAPIComponentRun maps a component-run row to the API list type (no logs).
func toAPIComponentRun(cr *ent.ComponentRun) ComponentRun {
	out := ComponentRun{
		Id:             cr.ID,
		Status:         ComponentRunStatus(cr.Status),
		Name:           optStr(cr.Name),
		Type:           optStr(cr.Type),
		Message:        optStr(cr.Message),
		RunName:        optStr(cr.RunName),
		ChartRevision:  optStr(cr.ChartRevision),
		ValuesRevision: optStr(cr.ValuesRevision),
		CreatedAt:      cr.CreatedAt,
		StartedAt:      cr.StartedAt,
		FinishedAt:     cr.FinishedAt,
		ApprovedAt:     cr.ApprovedAt,
	}
	if cr.ComponentID != uuid.Nil {
		id := cr.ComponentID
		out.ComponentId = &id
	}
	if cr.ApprovedBy != "" {
		out.ApprovedBy = &cr.ApprovedBy
	}
	return out
}

// toAPIComponentRunDetail is toAPIComponentRun plus the captured logs and the
// parsed preview diff. Both are derived from the run's captured pod logs, which
// echo the component's rendered Helm `values` / applied manifests — the same
// secret-bearing free-text the snapshot graph and component config redact below
// editor. So Logs and Diff are gated on canSee (editor-or-above): a viewer gets
// neither, matching redactGraph / redactConfig, rather than reading secrets out
// the side door of a component run.
//
// The diff is derived from the captured logs via helm.ParseDiff (the single
// parser for both helm and manifest previews, which emit identical sentinels):
// for a preview run it yields the diff body + whether deploying would change the
// cluster; a non-preview run has no markers, so it yields an empty body / false —
// the diff field is then simply omitted. has_changes is a non-secret boolean, so
// it is surfaced regardless of canSee.
func toAPIComponentRunDetail(cr *ent.ComponentRun, canSee bool) ComponentRunDetail {
	b := toAPIComponentRun(cr)
	out := ComponentRunDetail{
		Id:             b.Id,
		ComponentId:    b.ComponentId,
		Status:         b.Status,
		Name:           b.Name,
		Type:           b.Type,
		Message:        b.Message,
		RunName:        b.RunName,
		ChartRevision:  b.ChartRevision,
		ValuesRevision: b.ValuesRevision,
		CreatedAt:      b.CreatedAt,
		StartedAt:      b.StartedAt,
		FinishedAt:     b.FinishedAt,
	}
	diff := helm.ParseDiff(cr.Logs)
	if canSee {
		out.Logs = optStr(cr.Logs)
		if diff.Body != "" {
			body := diff.Body
			out.Diff = &body
		}
	}
	if diff.HasChanges {
		hc := true
		out.HasChanges = &hc
	}
	return out
}

// redactGraph strips secret-bearing config keys from each node of the stored
// graph snapshot for callers below editor (canSee=false). It round-trips the
// JSON through the snapshot type so the shape is exactly what the executor reads.
// An unparseable or empty snapshot returns "" (the field is then omitted).
func redactGraph(graph string, canSee bool) string {
	if graph == "" {
		return ""
	}
	if canSee {
		return graph
	}
	var snap workflows.GraphSnapshot
	if err := json.Unmarshal([]byte(graph), &snap); err != nil {
		// A snapshot we can't parse could hide secrets in an unexpected shape, so
		// withhold it entirely rather than risk leaking it to a viewer.
		return ""
	}
	for i := range snap.Nodes {
		for _, k := range secretConfigKeys {
			delete(snap.Nodes[i].Config, k)
		}
	}
	out, err := json.Marshal(snap)
	if err != nil {
		return ""
	}
	return string(out)
}
