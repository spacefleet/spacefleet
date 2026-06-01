package api

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/auth"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/secrets"
)

// defaultLogTail bounds the initial backlog a log stream replays before
// following, so opening logs on a chatty pod doesn't flood the client.
const defaultLogTail = 500

// maxLogLineBytes caps a single scanned log line; lines longer than this are
// split rather than overflowing the scanner.
const maxLogLineBytes = 1024 * 1024

// ListClusterPods returns a cluster's pods across all namespaces, mirroring
// ListClusterNodes. The UI normally consumes the live stream (StreamClusterPods)
// rather than this one-shot list, but it rounds out the REST contract and gives
// the generated Pod type a home.
func (s *Server) ListClusterPods(ctx context.Context, req ListClusterPodsRequestObject) (ListClusterPodsResponseObject, error) {
	orgID, aerr, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListClusterPodsdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	pods, err := s.clusters.Pods(ctx, orgID, req.Id)
	if err != nil {
		status, code, msg := podsFetchError(err)
		return errResp[ListClusterPodsdefaultJSONResponse](status, code, msg), nil
	}
	return ListClusterPods200JSONResponse(toAPIPods(pods)), nil
}

// StreamClusterPods streams a cluster's pods (across all namespaces) as
// Server-Sent Events, mirroring StreamClusterNodes: an initial `snapshot` then
// `added`/`modified`/`deleted` deltas. Namespace filtering is done client-side
// over the streamed set, so a single connection serves every namespace view.
func (s *Server) StreamClusterPods(w http.ResponseWriter, r *http.Request) {
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

	// Bound the stream to the credential that authorized it (see
	// StreamClusterNodes for the rationale).
	deadline := time.Now().Add(streamMaxLifetime)
	if sess, ok := auth.FromContext(ctx); ok && !sess.ExpiresAt.IsZero() && sess.ExpiresAt.Before(deadline) {
		deadline = sess.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	stream, err := s.clusters.WatchPods(ctx, orgID, id)
	if err != nil {
		status, code, msg := podsFetchError(err)
		writeStreamError(w, status, code, msg)
		return
	}

	sse, ok := newSSEWriter(w)
	if !ok {
		writeStreamError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}

	if err := sse.event(string(k8s.EventSnapshot), toAPIPods(stream.Snapshot)); err != nil {
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
			var payload any
			if ev.Type == k8s.EventSnapshot {
				payload = toAPIPods(ev.Snapshot)
			} else {
				payload = toAPIPod(ev.Object)
			}
			if err := sse.event(string(ev.Type), payload); err != nil {
				return
			}
		}
	}
}

// podsFetchError classifies a pod list/watch failure into a client-facing
// status, identically to nodesFetchError (the distinction between terminal and
// retriable failures matters for the live stream).
func podsFetchError(err error) (status int, code, msg string) {
	switch {
	case ent.IsNotFound(err):
		return http.StatusNotFound, "not_found", "cluster not found"
	case errors.Is(err, secrets.ErrDisabled):
		return http.StatusBadRequest, "encryption_unavailable", "this cluster has credentials but no encryption key is configured — set SPACEFLEET_SECRET_KEY"
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		return http.StatusForbidden, "cluster_forbidden", "the cluster's credentials are not authorized to list pods: " + err.Error()
	default:
		return http.StatusBadGateway, "cluster_unreachable", err.Error()
	}
}

func toAPIPod(p k8s.Pod) Pod {
	out := Pod{
		Name:            p.Name,
		Namespace:       p.Namespace,
		Phase:           p.Phase,
		Status:          p.Status,
		Ready:           p.Ready,
		ReadyContainers: p.ReadyContainers,
		TotalContainers: p.TotalContainers,
		Restarts:        p.Restarts,
		NodeName:        optStr(p.NodeName),
		PodIp:           optStr(p.PodIP),
		HostIp:          optStr(p.HostIP),
		QosClass:        optStr(p.QOSClass),
		ServiceAccount:  optStr(p.ServiceAccount),
		Labels:          p.Labels,
		Conditions:      make([]PodCondition, len(p.Conditions)),
		Containers:      make([]ContainerStatus, len(p.Containers)),
		CreatedAt:       p.CreatedAt,
	}
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	for i, c := range p.Conditions {
		pc := PodCondition{Type: c.Type, Status: c.Status, Reason: optStr(c.Reason), Message: optStr(c.Message)}
		pc.LastTransitionTime = c.LastTransitionTime
		out.Conditions[i] = pc
	}
	for i, c := range p.Containers {
		started := c.Started
		out.Containers[i] = ContainerStatus{
			Name:         c.Name,
			Image:        optStr(c.Image),
			Ready:        c.Ready,
			Started:      &started,
			RestartCount: c.RestartCount,
			State:        optStr(c.State),
			StateReason:  optStr(c.StateReason),
			StateMessage: optStr(c.StateMessage),
		}
	}
	return out
}

// toAPIPods maps a slice of domain pods to their API representation.
func toAPIPods(pods []k8s.Pod) []Pod {
	out := make([]Pod, len(pods))
	for i := range pods {
		out[i] = toAPIPod(pods[i])
	}
	return out
}

// logLine is the JSON payload of a `log` SSE event: one line of pod output.
type logLine struct {
	Line string `json:"line"`
}

// StreamPodLogs streams one pod's logs as Server-Sent Events: a `log` event per
// line (the last defaultLogTail lines first, then live as they're written),
// ending with an `eof` event when the log source closes (e.g. the container
// exits). It shares the same auth path and connection lifetime cap as the other
// streaming endpoints. Optional query params: `container` (required for
// multi-container pods), `tail` (initial backlog line count), `timestamps`.
func (s *Server) StreamPodLogs(w http.ResponseWriter, r *http.Request) {
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
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")
	if namespace == "" || name == "" {
		writeStreamError(w, http.StatusNotFound, "not_found", "pod not found")
		return
	}

	opts := k8s.LogOptions{
		Container:  r.URL.Query().Get("container"),
		Follow:     true,
		TailLines:  defaultLogTail,
		Timestamps: r.URL.Query().Get("timestamps") == "true",
	}
	if tail := r.URL.Query().Get("tail"); tail != "" {
		if n, perr := strconv.ParseInt(tail, 10, 64); perr == nil && n >= 0 {
			opts.TailLines = n
		}
	}

	// Bound the stream to the credential that authorized it (see
	// StreamClusterNodes for the rationale).
	deadline := time.Now().Add(streamMaxLifetime)
	if sess, ok := auth.FromContext(ctx); ok && !sess.ExpiresAt.IsZero() && sess.ExpiresAt.Before(deadline) {
		deadline = sess.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	// Open the log stream before switching to event-stream mode, so a bad
	// container name or unreachable cluster surfaces as a normal HTTP error.
	rc, err := s.clusters.PodLogs(ctx, orgID, id, namespace, name, opts)
	if err != nil {
		status, code, msg := podsFetchError(err)
		writeStreamError(w, status, code, msg)
		return
	}
	defer rc.Close()

	sse, ok := newSSEWriter(w)
	if !ok {
		writeStreamError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}

	// Scanning the log body blocks, so it runs in its own goroutine feeding a
	// channel; the main loop multiplexes lines with heartbeats and cancellation.
	// When the request context ends, the deferred Close unblocks the scanner.
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
				// The log source closed (container exited, or follow ended).
				_ = sse.event("eof", struct{}{})
				return
			}
			if err := sse.event("log", logLine{Line: line}); err != nil {
				return
			}
		}
	}
}
