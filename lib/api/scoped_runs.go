package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/tofu"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// StartComponentRun starts a run limited to one OpenTofu component: a
// per-component destroy (action uninstall — its apply is always gated) or a
// deploy of just that module, optionally targeted to a fixed list of
// resource addresses. Editor or above; needs the background worker (503
// otherwise). Another action, an invalid target, or a non-OpenTofu
// component is a 400, a component outside the org/app a 404, and a run
// already in flight a 409 — the same gate as every run.
func (s *Server) StartComponentRun(ctx context.Context, req StartComponentRunRequestObject) (StartComponentRunResponseObject, error) {
	orgID, aerr, err := s.resolveWorkflowWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[StartComponentRundefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[StartComponentRundefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	action := string(req.Body.Action)
	if action != workflows.ActionDeploy && action != workflows.ActionUninstall {
		return errResp[StartComponentRundefaultJSONResponse](http.StatusBadRequest, "bad_request", "action must be deploy or uninstall"), nil
	}
	var targets []string
	if req.Body.Targets != nil {
		targets = *req.Body.Targets
	}
	// Validate the targets before anything else so a bad request is a 400
	// even when no worker is configured.
	if err := tofu.ValidateTargets(targets); err != nil {
		return errResp[StartComponentRundefaultJSONResponse](http.StatusBadRequest, "bad_request", err.Error()), nil
	}
	if s.jobQueue == nil {
		return errResp[StartComponentRundefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "background job worker not configured; cannot start a run"), nil
	}
	run, err := s.workflows.BeginComponentRun(ctx, orgID, req.Id, req.ComponentId, action, targets)
	if err != nil {
		switch {
		case ent.IsNotFound(err):
			return errResp[StartComponentRundefaultJSONResponse](http.StatusNotFound, "not_found", "component not found"), nil
		case errors.Is(err, workflows.ErrNotTofuComponent):
			return errResp[StartComponentRundefaultJSONResponse](http.StatusBadRequest, "bad_request", "component-scoped runs apply only to OpenTofu components"), nil
		case errors.Is(err, workflows.ErrInvalidScopedAction), errors.Is(err, tofu.ErrInvalidTarget):
			return errResp[StartComponentRundefaultJSONResponse](http.StatusBadRequest, "bad_request", err.Error()), nil
		case errors.Is(err, workflows.ErrRunInFlight):
			return errResp[StartComponentRundefaultJSONResponse](http.StatusConflict, "conflict", err.Error()), nil
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
		Action:        action,
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
	return StartComponentRun202JSONResponse(toAPIWorkflowRun(run)), nil
}

// toAPIRunScope maps a component-scoped run's stored scope to the API
// shape. nil for a whole-workflow run, every other action, and a scoped
// run whose args don't decode (nothing to show).
func toAPIRunScope(r *ent.WorkflowRun) *RunScope {
	scope, ok, err := workflows.ScopeOf(r)
	if err != nil || !ok {
		return nil
	}
	out := &RunScope{ComponentId: scope.ComponentID, ComponentName: scope.ComponentName}
	if len(scope.Targets) > 0 {
		targets := scope.Targets
		out.Targets = &targets
	}
	return out
}
