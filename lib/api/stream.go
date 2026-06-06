package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/tektoninstallation"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
	"github.com/spacefleet/spacefleet/lib/auth"
	"github.com/spacefleet/spacefleet/lib/k8s"
)

// Streaming endpoints don't fit oapi-codegen's StrictServerInterface (which
// marshals a single typed response), so they're hand-written here and mounted
// manually on the mux behind the same RequireAuth + OrgContext middleware as the
// generated routes (see lib/server/routes.go). This is the one deliberate
// exception to "the OpenAPI spec is the source of truth".

const (
	// streamMaxLifetime caps a stream even when the token carries no expiry (or
	// a very distant one): a connection is recycled at least this often, which
	// also re-runs auth on the reconnect.
	streamMaxLifetime = 30 * time.Minute
	// streamHeartbeat is the idle keep-alive cadence — comfortably under the
	// 60s idle timeout common to proxies and load balancers.
	streamHeartbeat = 25 * time.Second
)

// StreamClusterNodes streams a cluster's nodes as Server-Sent Events: an initial
// `snapshot` event with the full list, then `added`/`modified`/`deleted` deltas
// as they happen, until the client disconnects or the stream's deadline passes.
//
// It is mounted manually (not via the generated router) but reuses the exact
// same authorization path as ListClusterNodes — resolve the user, authorize the
// org, scope the cluster to it — so the stream is not a security side door.
func (s *Server) StreamClusterNodes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	orgID, aerr, err := s.resolveOrg(ctx)
	if err != nil {
		writeStreamError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if aerr != nil {
		writeStreamError(w, aerr.status, aerr.code, aerr.msg)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeStreamError(w, http.StatusNotFound, "not_found", "cluster not found")
		return
	}

	// Bound the stream to the credential that authorized it: it never outlives
	// the token's expiry (nor streamMaxLifetime). When it ends the client
	// reconnects with a freshly-refreshed token.
	deadline := time.Now().Add(streamMaxLifetime)
	if sess, ok := auth.FromContext(ctx); ok && !sess.ExpiresAt.IsZero() && sess.ExpiresAt.Before(deadline) {
		deadline = sess.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	// Open the watch before switching to event-stream mode: its initial List
	// doubles as a reachability check, so an unreachable cluster surfaces as a
	// normal HTTP error rather than a half-open stream.
	stream, err := s.clusters.WatchNodes(ctx, orgID, id)
	if err != nil {
		status, code, msg := nodesFetchError(err)
		writeStreamError(w, status, code, msg)
		return
	}

	sse, ok := newSSEWriter(w)
	if !ok {
		writeStreamError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}

	// Initial snapshot.
	if err := sse.event(string(k8s.EventSnapshot), toAPINodes(stream.Snapshot)); err != nil {
		return
	}

	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if err := sse.comment("ping"); err != nil {
				return
			}
		case ev, ok := <-stream.Events:
			if !ok {
				// The watch ended (deadline or unrecoverable error); let the
				// client reconnect.
				return
			}
			var payload any
			if ev.Type == k8s.EventSnapshot {
				payload = toAPINodes(ev.Snapshot)
			} else {
				payload = toAPINode(ev.Object)
			}
			if err := sse.event(string(ev.Type), payload); err != nil {
				return
			}
		}
	}
}

// StreamClusterTekton streams a cluster's Tekton install status as Server-Sent
// Events: an initial `status` event, then a `status` event whenever the
// installation row changes, until a terminal state is reached (installed,
// failed, not_installed) or the client disconnects.
//
// This is the cross-process realtime path: the worker writes install progress to
// Postgres; this handler (in the serve process) tails the row on a short cadence
// and pushes each change to the browser. The browser only ever sees a live push
// stream — the tail is a serve-side detail. It reuses the same authorization
// path as GetClusterTekton, so the stream is not a security side door.
func (s *Server) StreamClusterTekton(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	orgID, aerr, err := s.resolveOrg(ctx)
	if err != nil {
		writeStreamError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if aerr != nil {
		writeStreamError(w, aerr.status, aerr.code, aerr.msg)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeStreamError(w, http.StatusNotFound, "not_found", "cluster not found")
		return
	}

	// The initial read doubles as an existence/authorization check, so a missing
	// cluster surfaces as a normal HTTP error rather than a half-open stream.
	row, err := s.clusters.TektonRow(ctx, orgID, id)
	if err != nil {
		status, code, msg := nodesFetchError(err)
		writeStreamError(w, status, code, msg)
		return
	}

	deadline := streamDeadline(ctx)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	sse, ok := newSSEWriter(w)
	if !ok {
		writeStreamError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	if err := sse.event("status", toAPITektonStatus(row, nil)); err != nil {
		return
	}
	if tektonTerminal(row) {
		return
	}

	prev := tektonRowKey(row)
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if err := sse.comment("ping"); err != nil {
				return
			}
		case <-poll.C:
			row, err := s.clusters.TektonRow(ctx, orgID, id)
			if err != nil {
				return
			}
			key := tektonRowKey(row)
			if key == prev {
				continue
			}
			prev = key
			if err := sse.event("status", toAPITektonStatus(row, nil)); err != nil {
				return
			}
			if tektonTerminal(row) {
				return
			}
		}
	}
}

// StreamClusterTektonRun streams a single TaskRun's status as Server-Sent
// Events: an initial `snapshot`, then `modified` events as the run progresses,
// until it reaches a terminal phase (Succeeded/Failed) or the client
// disconnects. It is driven by a live Tekton watch (genuinely event-driven).
func (s *Server) StreamClusterTektonRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	orgID, aerr, err := s.resolveOrg(ctx)
	if err != nil {
		writeStreamError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if aerr != nil {
		writeStreamError(w, aerr.status, aerr.code, aerr.msg)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeStreamError(w, http.StatusNotFound, "not_found", "cluster not found")
		return
	}
	name := r.PathValue("name")

	deadline := streamDeadline(ctx)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	stream, err := s.clusters.WatchRun(ctx, orgID, id, runNamespace, name)
	if err != nil {
		status, code, msg := nodesFetchError(err)
		writeStreamError(w, status, code, msg)
		return
	}

	sse, ok := newSSEWriter(w)
	if !ok {
		writeStreamError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	if err := sse.event("snapshot", toAPITektonRun(&stream.Snapshot)); err != nil {
		return
	}
	if stream.Snapshot.Terminal() {
		return
	}

	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if err := sse.comment("ping"); err != nil {
				return
			}
		case ev, ok := <-stream.Events:
			if !ok {
				return
			}
			run := ev.Object
			if err := sse.event("modified", toAPITektonRun(&run)); err != nil {
				return
			}
			if run.Terminal() {
				return
			}
		}
	}
}

// StreamApplicationRun streams a workflow run's status as Server-Sent Events: an
// initial `snapshot` event with the run plus its component runs, then a
// `snapshot` event whenever the run or any component run changes, until the run
// reaches a terminal status (succeeded, failed, partial) or the client
// disconnects. Like StreamClusterTekton, this is the cross-process realtime path:
// the workflow worker writes progress to Postgres; this handler (in serve) tails
// the rows and pushes each change. It reuses GetRun's auth path and redacts
// secret-bearing snapshot config below editor.
func (s *Server) StreamApplicationRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if s.workflows == nil {
		writeStreamError(w, http.StatusServiceUnavailable, "unavailable", "workflows service not configured")
		return
	}
	orgID, canSeeSecrets, aerr, err := s.resolveAppRead(ctx)
	if err != nil {
		writeStreamError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if aerr != nil {
		writeStreamError(w, aerr.status, aerr.code, aerr.msg)
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

	// The initial read doubles as an existence/authorization check.
	run, steps, err := s.workflows.GetRun(ctx, orgID, appID, runID)
	if err != nil {
		if ent.IsNotFound(err) {
			writeStreamError(w, http.StatusNotFound, "not_found", "run not found")
			return
		}
		writeStreamError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}

	ctx, cancel := context.WithDeadline(ctx, streamDeadline(ctx))
	defer cancel()

	sse, ok := newSSEWriter(w)
	if !ok {
		writeStreamError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	if err := sse.event("snapshot", toAPIWorkflowRunDetail(run, steps, canSeeSecrets)); err != nil {
		return
	}
	if runTerminal(run) {
		return
	}

	prev := runStateKey(run, steps)
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if err := sse.comment("ping"); err != nil {
				return
			}
		case <-poll.C:
			run, steps, err := s.workflows.GetRun(ctx, orgID, appID, runID)
			if err != nil {
				return
			}
			key := runStateKey(run, steps)
			if key == prev {
				continue
			}
			prev = key
			if err := sse.event("snapshot", toAPIWorkflowRunDetail(run, steps, canSeeSecrets)); err != nil {
				return
			}
			if runTerminal(run) {
				return
			}
		}
	}
}

// runStateKey is a change key over the run + its component runs' surfaced fields,
// so a no-op poll doesn't emit a redundant event but any step's progress does.
func runStateKey(run *ent.WorkflowRun, steps []*ent.ComponentRun) string {
	var b strings.Builder
	b.WriteString(string(run.Status))
	b.WriteByte(0)
	b.WriteString(run.Message)
	for _, cr := range steps {
		b.WriteByte(0)
		b.WriteString(cr.ID.String())
		b.WriteByte(':')
		b.WriteString(string(cr.Status))
		b.WriteByte(':')
		b.WriteString(cr.Message)
		b.WriteByte(':')
		b.WriteString(cr.RunName)
	}
	return b.String()
}

// runTerminal reports whether a workflow run has settled, so the stream can
// close.
func runTerminal(run *ent.WorkflowRun) bool {
	switch run.Status {
	case workflowrun.StatusSucceeded, workflowrun.StatusFailed, workflowrun.StatusPartial:
		return true
	default:
		return false
	}
}

// streamDeadline bounds a stream to the lesser of the token's expiry and
// streamMaxLifetime, so it never outlives the credential that authorized it.
func streamDeadline(ctx context.Context) time.Time {
	deadline := time.Now().Add(streamMaxLifetime)
	if sess, ok := auth.FromContext(ctx); ok && !sess.ExpiresAt.IsZero() && sess.ExpiresAt.Before(deadline) {
		deadline = sess.ExpiresAt
	}
	return deadline
}

// tektonRowKey is a change key over the install fields the stream surfaces, so a
// no-op poll doesn't emit a redundant event.
func tektonRowKey(row *ent.TektonInstallation) string {
	return string(row.Status) + "\x00" + row.StatusMessage + "\x00" + row.InstalledVersion + "\x00" + row.JobID
}

// tektonTerminal reports whether the install lifecycle has settled, so the
// stream can close.
func tektonTerminal(row *ent.TektonInstallation) bool {
	switch row.Status {
	case tektoninstallation.StatusInstalled, tektoninstallation.StatusFailed, tektoninstallation.StatusNotInstalled:
		return true
	default:
		return false
	}
}

// toAPINodes maps a slice of domain nodes to their API representation.
func toAPINodes(nodes []k8s.Node) []Node {
	out := make([]Node, len(nodes))
	for i := range nodes {
		out[i] = toAPINode(nodes[i])
	}
	return out
}

// writeStreamError renders a pre-stream failure as a normal JSON error, matching
// the Error schema the typed handlers use. Only valid before the event-stream
// headers are written.
func writeStreamError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// Marshal the proper Error struct so a message carrying a quote or backslash
	// (e.g. a raw k8s error) still produces valid JSON.
	body, err := json.Marshal(Error{Code: code, Message: msg})
	if err != nil {
		return
	}
	_, _ = w.Write(body)
}
