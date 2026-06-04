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
	orgID, aerr, err := s.resolveApp(ctx)
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
		out[i] = toAPIApplication(a)
	}
	return ListApplications200JSONResponse(out), nil
}

func (s *Server) GetApplication(ctx context.Context, req GetApplicationRequestObject) (GetApplicationResponseObject, error) {
	orgID, aerr, err := s.resolveApp(ctx)
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
	return GetApplication200JSONResponse(toAPIApplication(a)), nil
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
	a, aerr, err := s.beginRollout(ctx, orgID, req.Id, action)
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
	a, aerr, err := s.beginRollout(ctx, orgID, req.Id, helm.ActionUninstall)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[UninstallApplicationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	return UninstallApplication202JSONResponse(toAPIApplication(a)), nil
}

// beginRollout is the shared body of rollout + uninstall: require the worker,
// flip the app to the in-flight status, enqueue the job, and record the job id.
// It returns (app, nil, nil) on success, (_, *apiError, nil) for a client error
// to render, or (_, nil, err) for an internal error to bubble up.
func (s *Server) beginRollout(ctx context.Context, orgID, id uuid.UUID, action string) (*ent.Application, *apiError, error) {
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
	res, err := s.jobQueue.Insert(ctx, helm.RolloutArgs{ApplicationID: id, OrgID: orgID, Action: action})
	if err != nil {
		return nil, nil, err
	}
	jobID := strconv.FormatInt(res.Job.ID, 10)
	// Open this rollout's history record before the status flip below, so the
	// MarkRollout transitions (here and from the worker) find it by job id.
	if _, err := s.applications.RecordDeployment(ctx, orgID, id, action, jobID); err != nil {
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
		StatusMessage:   optStr(a.StatusMessage),
		JobId:           optStr(a.JobID),
		LastRunName:     optStr(a.LastRunName),
		CreatedAt:       a.CreatedAt,
		UpdatedAt:       a.UpdatedAt,
	}
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

// derefMap returns the pointed-to map, or nil.
func derefMap(p *map[string]string) map[string]string {
	if p == nil {
		return nil
	}
	return *p
}
