package api

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/auth"
	"github.com/spacefleet/spacefleet/lib/k8s"
)

// StreamComponentRunLogs streams one in-flight workflow step's pod logs as
// Server-Sent Events (a `log` event per line, then `eof` when the container
// exits) — the live counterpart of the captured `logs` a settled step carries.
// It resolves the step's TaskRun on the application's runner cluster by the
// run name the worker recorded, so the client never needs the cluster or pod.
//
// Editor or above only: a step's output echoes the same secret-bearing
// values the stored logs and diffs are gated on, so a viewer gets a 403 here
// just as they get no `logs` on the detail. A step that has not been
// submitted yet (no run name) or whose pod is already gone is a 404 — the
// client falls back to the captured logs once the step settles.
func (s *Server) StreamComponentRunLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if s.workflows == nil {
		writeStreamError(w, http.StatusServiceUnavailable, "unavailable", "workflows service not configured")
		return
	}
	orgID, canSee, aerr, err := s.resolveAppRead(ctx)
	if err != nil {
		writeStreamError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if aerr != nil {
		writeStreamError(w, aerr.status, aerr.code, aerr.msg)
		return
	}
	if !canSee {
		writeStreamError(w, http.StatusForbidden, "forbidden", "only an editor or admin can follow a step's logs")
		return
	}
	appID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeStreamError(w, http.StatusNotFound, "not_found", "application not found")
		return
	}
	runID, err := uuid.Parse(r.PathValue("runId"))
	if err != nil {
		writeStreamError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	crID, err := uuid.Parse(r.PathValue("componentRunId"))
	if err != nil {
		writeStreamError(w, http.StatusNotFound, "not_found", "component run not found")
		return
	}

	cr, err := s.workflows.GetComponentRun(ctx, orgID, appID, runID, crID)
	if err != nil {
		if ent.IsNotFound(err) {
			writeStreamError(w, http.StatusNotFound, "not_found", "component run not found")
			return
		}
		writeStreamError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if cr.RunName == "" {
		writeStreamError(w, http.StatusNotFound, "not_found", "this step has not started yet")
		return
	}
	if s.clusters == nil {
		writeStreamError(w, http.StatusServiceUnavailable, "unavailable", "clusters service not configured")
		return
	}
	app, err := s.applications.Get(ctx, orgID, appID)
	if err != nil {
		if ent.IsNotFound(err) {
			writeStreamError(w, http.StatusNotFound, "not_found", "application not found")
			return
		}
		writeStreamError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}

	// Bound the stream to the credential that authorized it (see
	// StreamClusterNodes for the rationale).
	deadline := time.Now().Add(streamMaxLifetime)
	if sess, ok := auth.FromContext(ctx); ok && !sess.ExpiresAt.IsZero() && sess.ExpiresAt.Before(deadline) {
		deadline = sess.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	// The TaskRun names the pod; both live on the runner cluster in the jobs
	// namespace, exactly where the worker submitted them.
	run, err := s.clusters.GetRun(ctx, orgID, app.RunnerClusterID, runNamespace, cr.RunName)
	if err != nil {
		status, code, msg := tektonRunFetchError(err)
		writeStreamError(w, status, code, msg)
		return
	}
	if run.PodName == "" {
		writeStreamError(w, http.StatusNotFound, "not_found", "this step's pod has not been scheduled yet")
		return
	}
	rc, err := s.clusters.PodLogs(ctx, orgID, app.RunnerClusterID, runNamespace, run.PodName, k8s.LogOptions{
		Follow:    true,
		TailLines: defaultLogTail,
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			writeStreamError(w, http.StatusNotFound, "not_found", "this step's pod is gone")
			return
		}
		status, code, msg := podsFetchError(err)
		writeStreamError(w, status, code, msg)
		return
	}
	defer rc.Close()

	pumpLogStream(ctx, w, rc)
}
