package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/tofu"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// StartStateOperation starts a guarded state operation (`state_op` run) on
// one OpenTofu component: force-unlock / state rm / state mv / import as a
// fixed, typed menu — never a free-form command. Editor or above; needs the
// background worker (503 otherwise). The run is one always-gated step, so
// it parks at awaiting_approval with the exact command on the run
// (state_op.command) for the approver to read. An invalid operation or a
// non-OpenTofu component is a 400, a component outside the org/app a 404, and
// a run already in flight a 409 — the same gate as every run, so a state
// operation never runs beside a deploy.
func (s *Server) StartStateOperation(ctx context.Context, req StartStateOperationRequestObject) (StartStateOperationResponseObject, error) {
	orgID, aerr, err := s.resolveWorkflowWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[StartStateOperationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[StartStateOperationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	op := tofu.StateOp{
		Operation:  string(req.Body.Operation),
		Address:    derefStr(req.Body.Address),
		NewAddress: derefStr(req.Body.NewAddress),
		LockID:     derefStr(req.Body.LockId),
		ImportID:   derefStr(req.Body.ImportId),
	}
	// Validate the typed fields before anything else so a bad request is a
	// 400 even when no worker is configured.
	if err := op.Validate(); err != nil {
		return errResp[StartStateOperationdefaultJSONResponse](http.StatusBadRequest, "bad_request", err.Error()), nil
	}
	if s.jobQueue == nil {
		return errResp[StartStateOperationdefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "background job worker not configured; cannot start a state operation"), nil
	}
	run, err := s.workflows.BeginStateOp(ctx, orgID, req.Id, req.ComponentId, op)
	if err != nil {
		switch {
		case ent.IsNotFound(err):
			return errResp[StartStateOperationdefaultJSONResponse](http.StatusNotFound, "not_found", "component not found"), nil
		case errors.Is(err, workflows.ErrNotTofuComponent):
			return errResp[StartStateOperationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "state operations apply only to OpenTofu components"), nil
		case errors.Is(err, tofu.ErrInvalidStateOp):
			return errResp[StartStateOperationdefaultJSONResponse](http.StatusBadRequest, "bad_request", err.Error()), nil
		case errors.Is(err, workflows.ErrRunInFlight):
			return errResp[StartStateOperationdefaultJSONResponse](http.StatusConflict, "conflict", err.Error()), nil
		default:
			return nil, err
		}
	}
	if err := s.recordRunStarter(ctx, orgID, run); err != nil {
		return nil, err
	}
	// Same non-atomic enqueue posture as StartRun: the pending run arms the
	// application's in-flight gate, so a failed Insert must fail the run rather
	// than leave it pending with no job behind it.
	res, err := s.jobQueue.Insert(ctx, workflows.WorkflowRunArgs{
		WorkflowRunID: run.ID,
		OrgID:         orgID,
		ApplicationID: req.Id,
		Action:        workflows.ActionStateOp,
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
	return StartStateOperation202JSONResponse(toAPIWorkflowRun(run)), nil
}

// toAPIStateOperation maps a state_op run's stored arguments to the API
// shape, with the exact command its step runs — rendered from the operation
// plus the snapshot node's plan flags (an import carries the -var/-var-file
// ones), so the approval gate and the history show what actually ran. nil
// for every other action, and for a state_op run whose args don't decode
// (the worker fails such a run; there is nothing to show).
func toAPIStateOperation(r *ent.WorkflowRun) *StateOperation {
	op, ok, err := workflows.StateOpOf(r)
	if err != nil || !ok {
		return nil
	}
	out := &StateOperation{
		Operation:  StateOperationKind(op.Operation),
		Address:    optStr(op.Address),
		NewAddress: optStr(op.NewAddress),
		LockId:     optStr(op.LockID),
		ImportId:   optStr(op.ImportID),
		Command:    op.Command(snapshotPlanFlags(r.Graph)),
	}
	return out
}

// snapshotPlanFlags reads the plan_flags of the first (only) node of a
// state_op run's snapshot — the component's plan-time flags as they were
// when the run began. Empty or unparseable input yields nil.
func snapshotPlanFlags(graph string) []string {
	if graph == "" {
		return nil
	}
	var snap workflows.GraphSnapshot
	if err := json.Unmarshal([]byte(graph), &snap); err != nil || len(snap.Nodes) == 0 {
		return nil
	}
	raw := snap.Nodes[0].Config["plan_flags"]
	if raw == "" {
		return nil
	}
	var flags []string
	if err := json.Unmarshal([]byte(raw), &flags); err != nil {
		return nil
	}
	return flags
}

// derefStr returns the pointed-to string, or "" for nil.
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
