package api

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

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
