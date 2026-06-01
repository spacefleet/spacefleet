package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/auth"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/secrets"
)

// ListClusterNamespaces returns a cluster's namespaces, mirroring
// ListClusterNodes. The UI normally consumes the live stream
// (StreamClusterNamespaces) rather than this one-shot list, but it rounds out
// the REST contract and gives the generated Namespace type a home.
func (s *Server) ListClusterNamespaces(ctx context.Context, req ListClusterNamespacesRequestObject) (ListClusterNamespacesResponseObject, error) {
	orgID, aerr, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListClusterNamespacesdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	namespaces, err := s.clusters.Namespaces(ctx, orgID, req.Id)
	if err != nil {
		status, code, msg := namespacesFetchError(err)
		return errResp[ListClusterNamespacesdefaultJSONResponse](status, code, msg), nil
	}
	return ListClusterNamespaces200JSONResponse(toAPINamespaces(namespaces)), nil
}

// StreamClusterNamespaces streams a cluster's namespaces as Server-Sent Events,
// mirroring StreamClusterNodes: an initial `snapshot` then
// `added`/`modified`/`deleted` deltas. Namespaces are a cluster-level resource,
// so a single connection serves the whole cluster.
func (s *Server) StreamClusterNamespaces(w http.ResponseWriter, r *http.Request) {
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

	stream, err := s.clusters.WatchNamespaces(ctx, orgID, id)
	if err != nil {
		status, code, msg := namespacesFetchError(err)
		writeStreamError(w, status, code, msg)
		return
	}

	sse, ok := newSSEWriter(w)
	if !ok {
		writeStreamError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}

	if err := sse.event(string(k8s.EventSnapshot), toAPINamespaces(stream.Snapshot)); err != nil {
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
				payload = toAPINamespaces(ev.Snapshot)
			} else {
				payload = toAPINamespace(ev.Object)
			}
			if err := sse.event(string(ev.Type), payload); err != nil {
				return
			}
		}
	}
}

// namespacesFetchError classifies a namespace list/watch failure into a
// client-facing status, identically to nodesFetchError (the distinction between
// terminal and retriable failures matters for the live stream).
func namespacesFetchError(err error) (status int, code, msg string) {
	switch {
	case ent.IsNotFound(err):
		return http.StatusNotFound, "not_found", "cluster not found"
	case errors.Is(err, secrets.ErrDisabled):
		return http.StatusBadRequest, "encryption_unavailable", "this cluster has credentials but no encryption key is configured — set SPACEFLEET_SECRET_KEY"
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		return http.StatusForbidden, "cluster_forbidden", "the cluster's credentials are not authorized to list namespaces: " + err.Error()
	default:
		return http.StatusBadGateway, "cluster_unreachable", err.Error()
	}
}

func toAPINamespace(n k8s.Namespace) Namespace {
	out := Namespace{
		Name:      n.Name,
		Status:    n.Status,
		Labels:    n.Labels,
		CreatedAt: n.CreatedAt,
	}
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	return out
}

// toAPINamespaces maps a slice of domain namespaces to their API representation.
func toAPINamespaces(namespaces []k8s.Namespace) []Namespace {
	out := make([]Namespace, len(namespaces))
	for i := range namespaces {
		out[i] = toAPINamespace(namespaces[i])
	}
	return out
}
