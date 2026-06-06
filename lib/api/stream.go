package api

import (
	"bufio"
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/application"
	"github.com/spacefleet/spacefleet/ent/tektoninstallation"
	"github.com/spacefleet/spacefleet/lib/auth"
	"github.com/spacefleet/spacefleet/lib/helm"
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

// StreamApplication streams an application's rollout status as Server-Sent
// Events: an initial `status` event, then a `status` event whenever the row
// changes, until a terminal state (deployed, failed, uninstalled) or the client
// disconnects. Like StreamClusterTekton, this is the cross-process realtime
// path: the rollout worker writes status to Postgres; this handler (in serve)
// tails the row and pushes each change. It reuses GetApplication's auth path.
func (s *Server) StreamApplication(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	orgID, aerr, err := s.resolveApp(ctx)
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
		writeStreamError(w, http.StatusNotFound, "not_found", "application not found")
		return
	}

	// The initial read doubles as an existence/authorization check.
	app, err := s.applications.Get(ctx, orgID, id)
	if err != nil {
		if ent.IsNotFound(err) {
			writeStreamError(w, http.StatusNotFound, "not_found", "application not found")
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
	if err := sse.event("status", toAPIApplication(app)); err != nil {
		return
	}
	if appTerminal(app) {
		return
	}

	prev := appRowKey(app)
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
			app, err := s.applications.Get(ctx, orgID, id)
			if err != nil {
				return
			}
			key := appRowKey(app)
			if key == prev {
				continue
			}
			prev = key
			if err := sse.event("status", toAPIApplication(app)); err != nil {
				return
			}
			if appTerminal(app) {
				return
			}
		}
	}
}

// StreamApplicationLogs streams the helm rollout pod's logs (the TaskRun pod on
// the runner cluster). It resolves the app's run → pod, then reuses the cluster
// pod-log stream. Mirrors StreamPodLogs.
func (s *Server) StreamApplicationLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	orgID, aerr, err := s.resolveApp(ctx)
	if err != nil {
		writeStreamError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if aerr != nil {
		writeStreamError(w, aerr.status, aerr.code, aerr.msg)
		return
	}
	app, aerr := s.appForStream(ctx, orgID, r)
	if aerr != nil {
		writeStreamError(w, aerr.status, aerr.code, aerr.msg)
		return
	}

	ctx, cancel := context.WithDeadline(ctx, streamDeadline(ctx))
	defer cancel()

	// The client passes the run it's viewing (?run=) so it reopens this stream
	// when a new rollout starts. Only the app's current run is streamable; a
	// stale name means the client is behind and should reconnect on the new one.
	if q := r.URL.Query().Get("run"); q != "" && q != app.LastRunName {
		writeStreamError(w, http.StatusConflict, "stale_run", "this run is no longer the current rollout")
		return
	}

	// Resolve the run's backing pod before switching to event-stream mode.
	run, err := s.clusters.GetRun(ctx, orgID, app.RunnerClusterID, helm.RunNamespace, app.LastRunName)
	if err != nil {
		status, code, msg := nodesFetchError(err)
		writeStreamError(w, status, code, msg)
		return
	}
	if run.PodName == "" {
		writeStreamError(w, http.StatusConflict, "no_pod", "the rollout has not started a pod yet")
		return
	}

	opts := k8s.LogOptions{Follow: true, TailLines: defaultLogTail, Timestamps: r.URL.Query().Get("timestamps") == "true"}
	if tail := r.URL.Query().Get("tail"); tail != "" {
		if n, perr := strconv.ParseInt(tail, 10, 64); perr == nil && n >= 0 {
			opts.TailLines = n
		}
	}

	rc, err := s.clusters.RunLogs(ctx, orgID, app.RunnerClusterID, helm.RunNamespace, run.PodName, opts)
	if err != nil {
		status, code, msg := nodesFetchError(err)
		writeStreamError(w, status, code, msg)
		return
	}
	defer rc.Close()

	sse, ok := newSSEWriter(w)
	if !ok {
		writeStreamError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}

	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(rc)
		sc.Buffer(make([]byte, 0, 64*1024), maxLogLineBytes)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

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
		case line, ok := <-lines:
			if !ok {
				_ = sse.event("eof", struct{}{})
				return
			}
			if err := sse.event("log", logLine{Line: line}); err != nil {
				return
			}
		}
	}
}

// appForStream resolves the application by the request's {id} path value and
// checks it has had a rollout (a last run name), returning a typed error
// otherwise. Shared by the run + logs streams.
func (s *Server) appForStream(ctx context.Context, orgID uuid.UUID, r *http.Request) (*ent.Application, *apiError) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return nil, &apiError{http.StatusNotFound, "not_found", "application not found"}
	}
	app, err := s.applications.Get(ctx, orgID, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, &apiError{http.StatusNotFound, "not_found", "application not found"}
		}
		return nil, &apiError{http.StatusInternalServerError, "internal", "internal error"}
	}
	if app.LastRunName == "" {
		return nil, &apiError{http.StatusConflict, "no_run", "this application has not been rolled out yet"}
	}
	return app, nil
}

// appRowKey is a change key over the rollout + sync fields the stream surfaces,
// so a no-op poll doesn't emit a redundant event but a refresh's progress does.
func appRowKey(a *ent.Application) string {
	return string(a.Status) + "\x00" + a.StatusMessage + "\x00" + a.JobID + "\x00" + a.LastRunName +
		"\x00" + string(a.SyncStatus) + "\x00" + a.SyncMessage + "\x00" + a.SyncJobID + "\x00" + a.SyncRunName
}

// appTerminal reports whether the stream can close: the rollout lifecycle has
// settled and no refresh (preview/diff) is in flight. A refresh runs on an
// already-deployed (rollout-terminal) app, so the stream must stay open while
// sync_status is refreshing to carry the diff's progress.
func appTerminal(a *ent.Application) bool {
	if a.SyncStatus == application.SyncStatusRefreshing {
		return false
	}
	switch a.Status {
	case application.StatusDeployed, application.StatusFailed, application.StatusUninstalled:
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
	_, _ = w.Write([]byte(`{"code":"` + code + `","message":"` + msg + `"}`))
}
