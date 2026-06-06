package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/lib/applications"
	"github.com/spacefleet/spacefleet/lib/helm"
)

// resolveApp runs the read preamble for application handlers: confirm the
// applications service exists, then resolve + authorize org membership. Mirrors
// resolveOrg for clusters.
func (s *Server) resolveApp(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.applications == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "applications service not configured"}, nil
	}
	m, aerr, err := s.resolveMembership(ctx)
	if err != nil || aerr != nil {
		return uuid.Nil, aerr, err
	}
	return m.OrganizationID, nil, nil
}

// resolveAppRead is the read preamble plus the caller's "can see secret-bearing
// free-text" capability: the stored Helm `values` is not sealed at rest (see the
// deferred-sealing note on redactAppSecrets) and "may contain secrets passed at
// install time", so it is only returned to editor-or-above callers. Read handlers
// that map an application row use this and pass the bool to redactAppSecrets;
// resolveApp stays as the plain membership preamble for callers that don't map a
// row (e.g. deployment history).
func (s *Server) resolveAppRead(ctx context.Context) (uuid.UUID, bool, *apiError, error) {
	if s.applications == nil {
		return uuid.Nil, false, &apiError{http.StatusServiceUnavailable, "unavailable", "applications service not configured"}, nil
	}
	m, aerr, err := s.resolveMembership(ctx)
	if err != nil || aerr != nil {
		return uuid.Nil, false, aerr, err
	}
	return m.OrganizationID, atLeast(m.Role, membership.RoleEditor), nil, nil
}

// resolveAppWrite is resolveApp plus an editor-or-above gate, for the handlers
// that change state (create, update, delete, rollout, uninstall).
func (s *Server) resolveAppWrite(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.applications == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "applications service not configured"}, nil
	}
	m, aerr, err := s.resolveMembership(ctx)
	if err != nil || aerr != nil {
		return uuid.Nil, aerr, err
	}
	if aerr := requireRole(m, membership.RoleEditor); aerr != nil {
		return uuid.Nil, aerr, nil
	}
	return m.OrganizationID, nil, nil
}

func (s *Server) ListApplications(ctx context.Context, _ ListApplicationsRequestObject) (ListApplicationsResponseObject, error) {
	orgID, canSeeSecrets, aerr, err := s.resolveAppRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListApplicationsdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	list, err := s.applications.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Application, len(list))
	for i, a := range list {
		out[i] = redactAppSecrets(toAPIApplication(a), canSeeSecrets)
	}
	return ListApplications200JSONResponse(out), nil
}

func (s *Server) GetApplication(ctx context.Context, req GetApplicationRequestObject) (GetApplicationResponseObject, error) {
	orgID, canSeeSecrets, aerr, err := s.resolveAppRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetApplicationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	a, err := s.applications.Get(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[GetApplicationdefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		}
		return nil, err
	}
	return GetApplication200JSONResponse(redactAppSecrets(toAPIApplication(a), canSeeSecrets)), nil
}

func (s *Server) CreateApplication(ctx context.Context, req CreateApplicationRequestObject) (CreateApplicationResponseObject, error) {
	orgID, aerr, err := s.resolveAppWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[CreateApplicationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[CreateApplicationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	name := strings.TrimSpace(req.Body.Name)
	if name == "" {
		return errResp[CreateApplicationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name is required"), nil
	}
	a, err := s.applications.Create(ctx, orgID, applications.CreateParams{
		Name:                 name,
		ChartSource:          string(req.Body.ChartSource),
		Config:               derefMap(req.Body.Config),
		Values:               deref(req.Body.Values),
		ValuesSources:        valuesSourcesToMaps(req.Body.ValuesSources),
		ReleaseName:          deref(req.Body.ReleaseName),
		TargetNamespace:      strings.TrimSpace(req.Body.TargetNamespace),
		TargetClusterID:      req.Body.TargetClusterId,
		RunnerClusterID:      req.Body.RunnerClusterId,
		ChartCredentialID:    req.Body.ChartCredentialId,
		GitHubInstallationID: req.Body.GithubInstallationId,
	})
	if err != nil {
		if resp, ok := appWriteError[CreateApplicationdefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return CreateApplication201JSONResponse(toAPIApplication(a)), nil
}

// ImportApplication adopts a release already running on the target cluster as a
// managed application (the import flow). It does NOT run a rollout — the release
// is already live, so the app is created in the deployed state — but, when the
// background worker is available, it enqueues a refresh so the sync status shows
// whether the configured chart source reproduces the live release. Editor or
// above.
func (s *Server) ImportApplication(ctx context.Context, req ImportApplicationRequestObject) (ImportApplicationResponseObject, error) {
	orgID, aerr, err := s.resolveAppWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ImportApplicationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[ImportApplicationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	name := strings.TrimSpace(req.Body.Name)
	if name == "" {
		return errResp[ImportApplicationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name is required"), nil
	}
	a, err := s.applications.Adopt(ctx, orgID, applications.ImportParams{
		Name:                 name,
		ChartSource:          string(req.Body.ChartSource),
		Config:               derefMap(req.Body.Config),
		Values:               deref(req.Body.Values),
		ValuesSources:        valuesSourcesToMaps(req.Body.ValuesSources),
		ReleaseName:          deref(req.Body.ReleaseName),
		TargetNamespace:      strings.TrimSpace(req.Body.TargetNamespace),
		TargetClusterID:      req.Body.TargetClusterId,
		RunnerClusterID:      req.Body.RunnerClusterId,
		ChartCredentialID:    req.Body.ChartCredentialId,
		GitHubInstallationID: req.Body.GithubInstallationId,
	})
	if err != nil {
		if resp, ok := appWriteError[ImportApplicationdefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	// Auto-refresh: kick off a `helm diff` so the operator immediately sees whether
	// the configured chart source reproduces the live release. Best-effort — an
	// adopt with no worker still succeeds (the app is already deployed); the user
	// can refresh later. Mirrors RefreshApplication's enqueue.
	if s.jobQueue != nil {
		if _, err := s.applications.BeginPreview(ctx, orgID, a.ID); err == nil {
			if res, err := s.jobQueue.Insert(ctx, helm.PreviewArgs{ApplicationID: a.ID, OrgID: orgID}); err == nil {
				jobID := strconv.FormatInt(res.Job.ID, 10)
				if err := s.applications.MarkPreview(ctx, orgID, a.ID, jobID, helm.SyncRefreshing, "queued for refresh", ""); err == nil {
					a.SyncJobID = jobID
					a.SyncStatus = "refreshing"
				}
			}
		}
	}
	return ImportApplication202JSONResponse(toAPIApplication(a)), nil
}

func (s *Server) UpdateApplication(ctx context.Context, req UpdateApplicationRequestObject) (UpdateApplicationResponseObject, error) {
	orgID, aerr, err := s.resolveAppWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[UpdateApplicationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[UpdateApplicationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	params := applications.UpdateParams{
		Values:      req.Body.Values,
		ReleaseName: req.Body.ReleaseName,
	}
	if req.Body.Name != nil {
		name := strings.TrimSpace(*req.Body.Name)
		if name == "" {
			return errResp[UpdateApplicationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name cannot be empty"), nil
		}
		params.Name = &name
	}
	if req.Body.TargetNamespace != nil {
		ns := strings.TrimSpace(*req.Body.TargetNamespace)
		params.TargetNamespace = &ns
	}
	if req.Body.Config != nil {
		params.Config = req.Body.Config
	}
	if req.Body.ValuesSources != nil {
		sources := valuesSourcesToMaps(req.Body.ValuesSources)
		params.ValuesSources = &sources
	}
	if req.Body.ChartCredentialId != nil {
		params.ChartCredentialID = req.Body.ChartCredentialId
	}
	if req.Body.GithubInstallationId != nil {
		params.GitHubInstallationID = req.Body.GithubInstallationId
	}
	a, err := s.applications.Update(ctx, orgID, req.Id, params)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[UpdateApplicationdefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		}
		if resp, ok := appWriteError[UpdateApplicationdefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return UpdateApplication200JSONResponse(toAPIApplication(a)), nil
}

func (s *Server) DeleteApplication(ctx context.Context, req DeleteApplicationRequestObject) (DeleteApplicationResponseObject, error) {
	orgID, aerr, err := s.resolveAppWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[DeleteApplicationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if err := s.applications.Delete(ctx, orgID, req.Id); err != nil {
		if ent.IsNotFound(err) {
			return errResp[DeleteApplicationdefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		}
		return nil, err
	}
	return DeleteApplication204Response{}, nil
}

// RolloutApplication enqueues a deploy/upgrade rollout. Editor or above; needs
// the background worker (503 otherwise) — mirrors EnableClusterTekton.
func (s *Server) RolloutApplication(ctx context.Context, req RolloutApplicationRequestObject) (RolloutApplicationResponseObject, error) {
	orgID, aerr, err := s.resolveAppWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[RolloutApplicationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[RolloutApplicationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	action := string(req.Body.Action)
	if action != helm.ActionDeploy && action != helm.ActionUpgrade {
		return errResp[RolloutApplicationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "action must be deploy or upgrade"), nil
	}
	force := req.Body.Force != nil && *req.Body.Force
	a, aerr, err := s.beginRollout(ctx, orgID, req.Id, action, force)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[RolloutApplicationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	return RolloutApplication202JSONResponse(toAPIApplication(a)), nil
}

// UninstallApplication enqueues an uninstall rollout. Editor or above; needs the
// background worker (503 otherwise).
func (s *Server) UninstallApplication(ctx context.Context, req UninstallApplicationRequestObject) (UninstallApplicationResponseObject, error) {
	orgID, aerr, err := s.resolveAppWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[UninstallApplicationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	a, aerr, err := s.beginRollout(ctx, orgID, req.Id, helm.ActionUninstall, false)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[UninstallApplicationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	return UninstallApplication202JSONResponse(toAPIApplication(a)), nil
}

// RefreshApplication enqueues a preview (diff) job: it re-resolves the desired
// state and runs `helm diff` against the live cluster, changing nothing. Editor
// or above; needs the background worker (503 otherwise). Refusing while a rollout
// is in flight surfaces as 409.
func (s *Server) RefreshApplication(ctx context.Context, req RefreshApplicationRequestObject) (RefreshApplicationResponseObject, error) {
	orgID, aerr, err := s.resolveAppWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[RefreshApplicationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if s.jobQueue == nil {
		return errResp[RefreshApplicationdefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "background job worker not configured; cannot refresh"), nil
	}
	a, err := s.applications.BeginPreview(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[RefreshApplicationdefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		}
		// The only validation error from BeginPreview is the in-flight-rollout gate.
		if applications.IsValidation(err) {
			return errResp[RefreshApplicationdefaultJSONResponse](http.StatusConflict, "conflict", err.Error()), nil
		}
		return nil, err
	}
	res, err := s.jobQueue.Insert(ctx, helm.PreviewArgs{ApplicationID: req.Id, OrgID: orgID})
	if err != nil {
		return nil, err
	}
	jobID := strconv.FormatInt(res.Job.ID, 10)
	if err := s.applications.MarkPreview(ctx, orgID, req.Id, jobID, helm.SyncRefreshing, "queued for refresh", ""); err != nil {
		return nil, err
	}
	a.SyncJobID = jobID
	return RefreshApplication202JSONResponse(toAPIApplication(a)), nil
}

// GetApplicationDiff returns the cached diff from the application's most recent
// refresh. Read access (viewer or above), like GetApplication.
func (s *Server) GetApplicationDiff(ctx context.Context, req GetApplicationDiffRequestObject) (GetApplicationDiffResponseObject, error) {
	orgID, aerr, err := s.resolveApp(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetApplicationDiffdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	a, err := s.applications.Get(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[GetApplicationDiffdefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		}
		return nil, err
	}
	return GetApplicationDiff200JSONResponse(ApplicationDiff{
		SyncStatus:            SyncStatus(a.SyncStatus),
		SyncMessage:           optStr(a.SyncMessage),
		Diff:                  optStr(a.LastDiff),
		DesiredChartRevision:  optStr(a.DesiredChartRevision),
		DesiredValuesRevision: optStr(a.DesiredValuesRevision),
		LastRefreshedAt:       optTime(a.LastRefreshedAt),
	}), nil
}

// beginRollout is the shared body of rollout + uninstall: require the worker,
// flip the app to the in-flight status, enqueue the job, and record the job id.
// It returns (app, nil, nil) on success, (_, *apiError, nil) for a client error
// to render, or (_, nil, err) for an internal error to bubble up.
func (s *Server) beginRollout(ctx context.Context, orgID, id uuid.UUID, action string, force bool) (*ent.Application, *apiError, error) {
	if s.jobQueue == nil {
		return nil, &apiError{http.StatusServiceUnavailable, "unavailable", "background job worker not configured; cannot run a rollout"}, nil
	}
	a, err := s.applications.BeginRollout(ctx, orgID, id, action)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, &apiError{http.StatusNotFound, "not_found", "application not found"}, nil
		}
		if applications.IsValidation(err) {
			return nil, &apiError{http.StatusBadRequest, "bad_request", err.Error()}, nil
		}
		return nil, nil, err
	}
	// Best-effort, non-atomic by design: the queue Insert and the two DB writes
	// below (RecordDeployment, MarkRollout) are not in one transaction, so a
	// failure after the job is queued returns 500 with the job already enqueued.
	// We don't compensate (no dequeue/rollback) — the worker self-heals: it
	// reconciles desired vs. live state from the row by job id, so a job that runs
	// against a half-written row still converges, and a stuck in-flight status is
	// recoverable by re-issuing the rollout. Don't restructure into a transaction
	// (the queue isn't part of the ent tx) without that being the explicit goal.
	res, err := s.jobQueue.Insert(ctx, helm.RolloutArgs{ApplicationID: id, OrgID: orgID, Action: action, Force: force})
	if err != nil {
		return nil, nil, err
	}
	jobID := strconv.FormatInt(res.Job.ID, 10)
	// Open this rollout's history record before the status flip below, so the
	// MarkRollout transitions (here and from the worker) find it by job id.
	if _, err := s.applications.RecordDeployment(ctx, orgID, id, action, jobID, force); err != nil {
		return nil, nil, err
	}
	if err := s.applications.MarkRollout(ctx, orgID, id, jobID, statusForAction(action), "queued", ""); err != nil {
		return nil, nil, err
	}
	a.JobID = jobID
	return a, nil, nil
}

// statusForAction maps a rollout action to its in-flight status string.
func statusForAction(action string) string {
	if action == helm.ActionUninstall {
		return helm.StatusUninstalling
	}
	return helm.StatusDeploying
}

// appWriteError maps service-layer write errors to a typed client error, or
// reports false to bubble up as 500.
func appWriteError[T defaultResp](err error) (T, bool) {
	switch {
	case applications.IsValidation(err):
		return errResp[T](http.StatusBadRequest, "bad_request", err.Error()), true
	case ent.IsConstraintError(err):
		return errResp[T](http.StatusConflict, "conflict", "an application with that name already exists in this organization"), true
	default:
		var zero T
		return zero, false
	}
}

func toAPIApplication(a *ent.Application) Application {
	out := Application{
		Id:              a.ID,
		Name:            a.Name,
		Type:            ApplicationType(a.Type),
		ChartSource:     ChartSource(a.ChartSource),
		Config:          a.Config,
		TargetNamespace: a.TargetNamespace,
		TargetClusterId: a.TargetClusterID,
		RunnerClusterId: a.RunnerClusterID,
		Status:          ApplicationStatus(a.Status),
		ReleaseName:     optStr(a.ReleaseName),
		Values:          optStr(a.Values),
		ValuesSources:   valuesSourcesFromMaps(a.ValuesSources),
		StatusMessage:   optStr(a.StatusMessage),
		JobId:           optStr(a.JobID),
		LastRunName:     optStr(a.LastRunName),
		// Sync (preview/diff) summary. The full diff text is not on the row response
		// (it can be large) — it's fetched via GET .../diff.
		SyncMessage:           optStr(a.SyncMessage),
		DesiredChartRevision:  optStr(a.DesiredChartRevision),
		DesiredValuesRevision: optStr(a.DesiredValuesRevision),
		LastRefreshedAt:       optTime(a.LastRefreshedAt),
		SyncRunName:           optStr(a.SyncRunName),
		Imported:              &a.Imported,
		CreatedAt:             a.CreatedAt,
		UpdatedAt:             a.UpdatedAt,
	}
	syncStatus := SyncStatus(a.SyncStatus)
	out.SyncStatus = &syncStatus
	if out.Config == nil {
		out.Config = map[string]string{}
	}
	if a.ChartCredentialID != uuid.Nil {
		id := a.ChartCredentialID
		out.ChartCredentialId = &id
	}
	if a.GithubInstallationID != uuid.Nil {
		id := a.GithubInstallationID
		out.GithubInstallationId = &id
	}
	return out
}

// redactAppSecrets strips secret-bearing free-text from a mapped application
// before it is returned to a caller who isn't editor-or-above. Today that's the
// raw Helm `values` override, which "may contain secrets passed at install time"
// and is NOT sealed at rest (sealing it would need a data migration — deferred,
// out of scope here), so the read path must withhold it from viewers. Editors and
// above still receive it (canSee=true) so the edit-form prefill keeps working;
// write handlers map the row directly and are already editor-gated. If more
// secret-bearing columns are added, strip them here too.
func redactAppSecrets(out Application, canSee bool) Application {
	if !canSee {
		out.Values = nil
	}
	return out
}

// derefMap returns the pointed-to map, or nil.
func derefMap(p *map[string]string) map[string]string {
	if p == nil {
		return nil
	}
	return *p
}

// valuesSourcesToMaps converts API values sources to the storage shape
// ([]map[string]string keyed by helm.ValuesSource*). A nil input stays nil
// ("unchanged" / "none"); a non-nil (even empty) input maps element-for-element.
func valuesSourcesToMaps(in *[]ValuesSource) []map[string]string {
	if in == nil {
		return nil
	}
	out := make([]map[string]string, len(*in))
	for i, s := range *in {
		m := map[string]string{
			helm.ValuesSourceRepoURL: s.RepoUrl,
			helm.ValuesSourcePath:    s.Path,
		}
		if s.GitRef != nil && *s.GitRef != "" {
			m[helm.ValuesSourceGitRef] = *s.GitRef
		}
		out[i] = m
	}
	return out
}

// valuesSourcesFromMaps converts the stored shape back to API values sources for
// a response. An empty list maps to nil so the field is simply omitted.
func valuesSourcesFromMaps(in []map[string]string) *[]ValuesSource {
	if len(in) == 0 {
		return nil
	}
	out := make([]ValuesSource, len(in))
	for i, m := range in {
		vs := ValuesSource{
			RepoUrl: m[helm.ValuesSourceRepoURL],
			Path:    m[helm.ValuesSourcePath],
		}
		if ref := m[helm.ValuesSourceGitRef]; ref != "" {
			vs.GitRef = &ref
		}
		out[i] = vs
	}
	return &out
}
