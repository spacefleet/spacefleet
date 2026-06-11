package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/clusters"
	"github.com/spacefleet/spacefleet/lib/secrets"
	"github.com/spacefleet/spacefleet/lib/tekton"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// runNamespace is where minimal TaskRuns are submitted: the dedicated jobs
// namespace the managed Tekton install creates (a Tekton installed outside
// Spacefleet needs it created by hand). A richer per-org namespace is a later
// concern.
const runNamespace = tekton.JobsNamespace

// GetClusterTekton reports a cluster's job-running (Tekton) status: the stored
// install lifecycle reconciled with a live presence detection. Open to any
// member; an unreachable cluster is classified like a node-list failure.
func (s *Server) GetClusterTekton(ctx context.Context, req GetClusterTektonRequestObject) (GetClusterTektonResponseObject, error) {
	orgID, aerr, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetClusterTektondefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	row, presence, terr := s.clusters.TektonStatus(ctx, orgID, req.Id)
	if terr != nil {
		status, code, msg := nodesFetchError(terr)
		return errResp[GetClusterTektondefaultJSONResponse](status, code, msg), nil
	}
	return GetClusterTekton200JSONResponse(toAPITektonStatus(row, presence)), nil
}

// EnableClusterTekton designates a cluster to run jobs and, when Tekton isn't
// installed, enqueues the install job. Editor or above. Installing requires the
// background worker — if the job queue is unavailable and an install is needed,
// it returns 503 without changing state (so the cluster isn't left stuck in
// "installing" with nothing to run it).
func (s *Server) EnableClusterTekton(ctx context.Context, req EnableClusterTektonRequestObject) (EnableClusterTektonResponseObject, error) {
	orgID, aerr, err := s.resolveClusterWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[EnableClusterTektondefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}

	// Decide whether an install is needed from a *live* detection, not the stored
	// row: Tekton may have been removed out-of-band while the row still reads
	// "installed", so a stale row is not proof of presence. We (re-)assert the
	// enable flag and enqueue an idempotent install whenever the controller isn't
	// confirmed live; a cluster whose controller is ready is left untouched, so
	// re-asserting enable on a truly-installed cluster stays a cheap no-op.
	_, presence, err := s.clusters.TektonStatus(ctx, orgID, req.Id)
	if err != nil {
		status, code, msg := nodesFetchError(err)
		return errResp[EnableClusterTektondefaultJSONResponse](status, code, msg), nil
	}
	needsInstall := presence == nil || !presence.ControllerReady
	if needsInstall && s.jobQueue == nil {
		return errResp[EnableClusterTektondefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "background job worker not configured; cannot install Tekton"), nil
	}

	row, err := s.clusters.EnableTekton(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[EnableClusterTektondefaultJSONResponse](http.StatusNotFound, "not_found", "cluster not found"), nil
		}
		return nil, err
	}

	if needsInstall {
		res, err := s.jobQueue.Insert(ctx, tekton.InstallArgs{ClusterID: req.Id, OrgID: orgID, Action: tekton.ActionInstall})
		if err != nil {
			return nil, err
		}
		jobID := strconv.FormatInt(res.Job.ID, 10)
		if err := s.clusters.MarkTekton(ctx, orgID, req.Id, jobID, tekton.StatusInstalling, "", "queued for install"); err != nil {
			return nil, err
		}
		row.JobID = jobID
	}
	return EnableClusterTekton202JSONResponse(toAPITektonStatus(row, nil)), nil
}

// DisableClusterTekton clears the job-runner flag (it does not uninstall
// Tekton). Editor or above.
func (s *Server) DisableClusterTekton(ctx context.Context, req DisableClusterTektonRequestObject) (DisableClusterTektonResponseObject, error) {
	orgID, aerr, err := s.resolveClusterWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[DisableClusterTektondefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	row, err := s.clusters.DisableTekton(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[DisableClusterTektondefaultJSONResponse](http.StatusNotFound, "not_found", "cluster not found"), nil
		}
		return nil, err
	}
	return DisableClusterTekton200JSONResponse(toAPITektonStatus(row, nil)), nil
}

// UpgradeClusterTekton re-applies the full install manifest set to a
// Spacefleet-managed install: it brings the install to the pinned version,
// syncs a footprint that drifted from this binary's ManifestRevision, and
// repairs a failed/partial install. Editor or above. Refuses (409) a Tekton
// installed outside Spacefleet or one that isn't installed; needs the
// background worker.
func (s *Server) UpgradeClusterTekton(ctx context.Context, req UpgradeClusterTektonRequestObject) (UpgradeClusterTektonResponseObject, error) {
	orgID, aerr, err := s.resolveClusterWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[UpgradeClusterTektondefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if s.jobQueue == nil {
		return errResp[UpgradeClusterTektondefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "background job worker not configured; cannot upgrade Tekton"), nil
	}

	row, err := s.clusters.BeginTektonUpgrade(ctx, orgID, req.Id)
	if err != nil {
		if status, code, msg, ok := tektonLifecycleError(err); ok {
			return errResp[UpgradeClusterTektondefaultJSONResponse](status, code, msg), nil
		}
		status, code, msg := nodesFetchError(err)
		return errResp[UpgradeClusterTektondefaultJSONResponse](status, code, msg), nil
	}

	res, err := s.jobQueue.Insert(ctx, tekton.InstallArgs{ClusterID: req.Id, OrgID: orgID, Action: tekton.ActionUpgrade})
	if err != nil {
		return nil, err
	}
	jobID := strconv.FormatInt(res.Job.ID, 10)
	if err := s.clusters.MarkTekton(ctx, orgID, req.Id, jobID, tekton.StatusUpgrading, "", "queued for upgrade"); err != nil {
		return nil, err
	}
	row.JobID = jobID
	return UpgradeClusterTekton202JSONResponse(toAPITektonStatus(row, nil)), nil
}

// UninstallClusterTekton removes the Tekton release Spacefleet installed and
// clears the job-runner flag. Editor or above. Refuses (409) a Tekton installed
// outside Spacefleet, which it never removes; needs the background worker.
func (s *Server) UninstallClusterTekton(ctx context.Context, req UninstallClusterTektonRequestObject) (UninstallClusterTektonResponseObject, error) {
	orgID, aerr, err := s.resolveClusterWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[UninstallClusterTektondefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if s.jobQueue == nil {
		return errResp[UninstallClusterTektondefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "background job worker not configured; cannot remove Tekton"), nil
	}

	row, err := s.clusters.BeginTektonUninstall(ctx, orgID, req.Id)
	if err != nil {
		if status, code, msg, ok := tektonLifecycleError(err); ok {
			return errResp[UninstallClusterTektondefaultJSONResponse](status, code, msg), nil
		}
		status, code, msg := nodesFetchError(err)
		return errResp[UninstallClusterTektondefaultJSONResponse](status, code, msg), nil
	}

	res, err := s.jobQueue.Insert(ctx, tekton.InstallArgs{ClusterID: req.Id, OrgID: orgID, Action: tekton.ActionUninstall})
	if err != nil {
		return nil, err
	}
	jobID := strconv.FormatInt(res.Job.ID, 10)
	if err := s.clusters.MarkTekton(ctx, orgID, req.Id, jobID, tekton.StatusUninstalling, "", "queued for removal"); err != nil {
		return nil, err
	}
	row.JobID = jobID
	return UninstallClusterTekton202JSONResponse(toAPITektonStatus(row, nil)), nil
}

// tektonLifecycleError maps the upgrade/uninstall precondition errors to a typed
// HTTP error, reporting ok=false for anything else (a not-found cluster or an
// unreachable-cluster detection failure, which the caller maps separately).
func tektonLifecycleError(err error) (status int, code, msg string, ok bool) {
	switch {
	case ent.IsNotFound(err):
		return http.StatusNotFound, "not_found", "cluster not found", true
	case clusters.ErrTektonNotManaged(err):
		return http.StatusConflict, "not_managed", "Tekton in this cluster was not installed by Spacefleet", true
	case clusters.ErrTektonNotInstalled(err):
		return http.StatusConflict, "not_installed", "Tekton is not installed in this cluster", true
	default:
		return 0, "", "", false
	}
}

// SubmitClusterTektonRun submits a minimal single-step TaskRun. Editor or above.
func (s *Server) SubmitClusterTektonRun(ctx context.Context, req SubmitClusterTektonRunRequestObject) (SubmitClusterTektonRunResponseObject, error) {
	orgID, aerr, err := s.resolveClusterWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[SubmitClusterTektonRundefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[SubmitClusterTektonRundefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	name := sanitizeRunName(req.Body.Name)
	image := strings.TrimSpace(req.Body.Image)
	script := req.Body.Script
	if name == "" || image == "" || strings.TrimSpace(script) == "" {
		return errResp[SubmitClusterTektonRundefaultJSONResponse](http.StatusBadRequest, "bad_request", "name, image, and script are required"), nil
	}
	run, err := s.clusters.SubmitRun(ctx, orgID, req.Id, runNamespace, tekton.RunSpec{Name: name, Image: image, Script: script})
	if err != nil {
		status, code, msg := nodesFetchError(err)
		return errResp[SubmitClusterTektonRundefaultJSONResponse](status, code, msg), nil
	}
	return SubmitClusterTektonRun201JSONResponse(toAPITektonRun(run)), nil
}

// GetClusterTektonRun returns one TaskRun's current status.
func (s *Server) GetClusterTektonRun(ctx context.Context, req GetClusterTektonRunRequestObject) (GetClusterTektonRunResponseObject, error) {
	orgID, aerr, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetClusterTektonRundefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	run, err := s.clusters.GetRun(ctx, orgID, req.Id, runNamespace, req.Name)
	if err != nil {
		status, code, msg := tektonRunFetchError(err)
		return errResp[GetClusterTektonRundefaultJSONResponse](status, code, msg), nil
	}
	return GetClusterTektonRun200JSONResponse(toAPITektonRun(run)), nil
}

// tektonRunFetchError classifies a single-run lookup failure for the client. It
// adds the case nodesFetchError lacks — a missing TaskRun (apierrors.IsNotFound)
// is a 404, not a 502 cluster_unreachable — and never echoes the raw upstream
// error back to the caller (the "tekton: get taskrun: ..." detail is for logs,
// not the API response). A truly unreachable cluster still reads as 502.
func tektonRunFetchError(err error) (status int, code, msg string) {
	switch {
	case ent.IsNotFound(err):
		return http.StatusNotFound, "not_found", "cluster not found"
	case apierrors.IsNotFound(err):
		return http.StatusNotFound, "not_found", "run not found"
	case errors.Is(err, secrets.ErrDisabled):
		return http.StatusBadRequest, "encryption_unavailable", "this cluster has credentials but no encryption key is configured — set SPACEFLEET_SECRET_KEY"
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		return http.StatusForbidden, "cluster_forbidden", "the cluster's credentials are not authorized to read this run"
	default:
		return http.StatusBadGateway, "cluster_unreachable", "the cluster is currently unreachable"
	}
}

// toAPITektonStatus maps the installation row (and an optional live presence) to
// the API type. presence is nil for enable/disable responses, which carry stored
// state only; a status read or the stream supplies live presence.
func toAPITektonStatus(row *ent.TektonInstallation, p *tekton.Presence) TektonStatus {
	out := TektonStatus{
		Enabled:          row.Enabled,
		Status:           TektonInstallStatus(row.Status),
		StatusMessage:    optStr(row.StatusMessage),
		InstalledVersion: optStr(row.InstalledVersion),
		JobId:            optStr(row.JobID),
		LastCheckedAt:    row.LastCheckedAt,
		PinnedVersion:    tekton.PinnedVersion,
		ExpectedRevision: tekton.ManifestRevision(),
	}
	if p != nil {
		out.Present = p.Installed
		out.ControllerReady = p.ControllerReady
		out.Managed = p.Managed
		out.DetectedVersion = optStr(p.Version)
		out.DetectedRevision = optStr(p.Revision)
		// A managed install is out of date when its running version or its
		// stamped manifest revision differs from what this binary would apply —
		// the revision catches footprint changes (new namespaces, roles, …)
		// that don't bump the pinned Tekton version, including installs that
		// predate revision stamping (empty revision). Never offered for an
		// unmanaged install: upgrade is gated to Spacefleet-managed Tekton.
		out.UpdateAvailable = p.Installed && p.Managed &&
			(p.Revision != tekton.ManifestRevision() ||
				(p.Version != "" && p.Version != tekton.PinnedVersion))
	}
	return out
}

// toAPITektonRun maps a run status to the API type.
func toAPITektonRun(r *tekton.RunStatus) TektonRun {
	return TektonRun{
		Name:        r.Name,
		Namespace:   r.Namespace,
		Phase:       TektonRunPhase(r.Phase),
		PodName:     optStr(r.PodName),
		Message:     optStr(r.Message),
		StartedAt:   r.StartedAt,
		CompletedAt: r.CompletedAt,
	}
}

// sanitizeRunName lowercases the requested name and replaces any character that
// isn't a DNS-1123 label character with a hyphen, so it's a valid TaskRun
// generateName prefix. An empty result is rejected by the caller.
func sanitizeRunName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == ' ' || r == '_':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
