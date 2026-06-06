package api

import (
	"context"
	"errors"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/secrets"
)

// ListClusterReleases lists the Helm releases installed on a cluster, decoded
// from its release Secrets. It is the discovery step behind importing an
// existing release as an application. Unlike ListClusterNamespaces this is an
// editor-or-above operation, not read access: a release's decoded values (the
// `helm get values` output) routinely contain plaintext secrets, so it must be
// gated like the import write it feeds — a viewer gets 403, not the values.
func (s *Server) ListClusterReleases(ctx context.Context, req ListClusterReleasesRequestObject) (ListClusterReleasesResponseObject, error) {
	orgID, aerr, err := s.resolveClusterWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListClusterReleasesdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	namespace := ""
	if req.Params.Namespace != nil {
		namespace = *req.Params.Namespace
	}
	releases, err := s.clusters.Releases(ctx, orgID, req.Id, namespace)
	if err != nil {
		status, code, msg := releasesFetchError(err)
		return errResp[ListClusterReleasesdefaultJSONResponse](status, code, msg), nil
	}
	return ListClusterReleases200JSONResponse(toAPIReleases(releases)), nil
}

// releasesFetchError classifies a release-listing failure into a client-facing
// status. It mirrors namespacesFetchError but reflects that the operation reads
// Helm release storage (Secrets), not namespaces, so the forbidden message
// points at the right RBAC requirement.
func releasesFetchError(err error) (status int, code, msg string) {
	switch {
	case ent.IsNotFound(err):
		return http.StatusNotFound, "not_found", "cluster not found"
	case errors.Is(err, secrets.ErrDisabled):
		return http.StatusBadRequest, "encryption_unavailable", "this cluster has credentials but no encryption key is configured — set SPACEFLEET_SECRET_KEY"
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		return http.StatusForbidden, "cluster_forbidden", "the cluster's credentials are not authorized to read Helm release storage (list Secrets): " + err.Error()
	default:
		return http.StatusBadGateway, "cluster_unreachable", err.Error()
	}
}

func toAPIRelease(r k8s.Release) HelmRelease {
	out := HelmRelease{
		Name:         r.Name,
		Namespace:    r.Namespace,
		ChartName:    r.ChartName,
		ChartVersion: r.ChartVersion,
		Status:       r.Status,
		Revision:     r.Revision,
		AppVersion:   optStr(r.AppVersion),
		Values:       optStr(r.Values),
	}
	if !r.Updated.IsZero() {
		t := r.Updated
		out.UpdatedAt = &t
	}
	return out
}

// toAPIReleases maps a slice of discovered releases to their API representation.
func toAPIReleases(releases []k8s.Release) []HelmRelease {
	out := make([]HelmRelease, len(releases))
	for i := range releases {
		out[i] = toAPIRelease(releases[i])
	}
	return out
}
