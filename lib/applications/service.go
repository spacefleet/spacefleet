// Package applications holds the application (Helm release) use cases:
// registering an app in an organization, listing/fetching/updating/deleting
// them, and driving rollouts (deploy/upgrade/uninstall) as Tekton TaskRuns on a
// runner cluster. It is a thin wrapper over the ent client; domain logic for the
// rollout script lives in lib/helm and connection resolution in lib/clusters
// (via the narrow ConnResolver seam).
//
// Like every org-scoped resource, every query is scoped by organization id —
// that scoping, not the handler's membership check, is the security boundary.
// An application stores no secrets of its own (v1 is public charts only); the
// credentials it needs at rollout time are the *clusters'* sealed credentials,
// re-opened through the ConnResolver.
package applications

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/application"
	"github.com/spacefleet/spacefleet/ent/chartcredential"
	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/ent/deployment"
	"github.com/spacefleet/spacefleet/ent/githubinstallation"
	"github.com/spacefleet/spacefleet/lib/chartcredentials"
	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/tekton"
)

// ConnResolver resolves the decrypted connection for an org-scoped cluster. It
// is the narrow slice of *clusters.Service the rollout needs; defining it here
// keeps lib/applications free of an import on lib/clusters (and the dependency
// one-way).
type ConnResolver interface {
	ConnForTekton(ctx context.Context, orgID, clusterID uuid.UUID) (k8s.Connection, error)
}

// CredentialResolver decrypts an org-scoped chart credential for a rollout. It is
// the narrow slice of *chartcredentials.Service the rollout needs to inject
// private-chart auth; may be nil (no chart-credentials service wired), in which
// case an app referencing a credential fails the rollout with a clear error.
type CredentialResolver interface {
	Resolve(ctx context.Context, orgID, id uuid.UUID) (chartcredentials.Resolved, error)
}

// GitTokenResolver mints a short-lived GitHub App installation token for an
// org-scoped installation, for the rollout to authenticate a private-Git chart
// clone. It is the narrow slice of *githubinstallations.Service the rollout
// needs; may be nil (no GitHub App / installations service wired), in which case
// an app referencing an installation fails the rollout with a clear error.
type GitTokenResolver interface {
	InstallationToken(ctx context.Context, orgID, id uuid.UUID) (string, error)
}

// Service is a thin wrapper over the ent client plus the connection resolver.
type Service struct {
	ent       *ent.Client
	conns     ConnResolver
	creds     CredentialResolver
	gitTokens GitTokenResolver

	// captureLogs reads a terminal run's full output for its deployment record.
	// A seam over the runner-cluster interaction (resolve conn → find pod → read
	// logs): defaults to the real path below, overridden in tests to avoid a live
	// cluster. Returns "" on any failure (best-effort; never fails the rollout).
	captureLogs func(ctx context.Context, app *ent.Application, runName string) string
}

func NewService(entClient *ent.Client, conns ConnResolver, creds CredentialResolver, gitTokens GitTokenResolver) *Service {
	svc := &Service{ent: entClient, conns: conns, creds: creds, gitTokens: gitTokens}
	svc.captureLogs = svc.fetchRunLogs
	return svc
}

// fetchRunLogs is the default captureLogs: it resolves the runner connection,
// finds the run's backing pod, and reads its logs in full (no follow), so the
// run stays readable after the TaskRun pod is garbage-collected. Best-effort —
// any failure (no pod, unreachable runner, read error) yields "".
func (s *Service) fetchRunLogs(ctx context.Context, app *ent.Application, runName string) string {
	if runName == "" {
		return ""
	}
	conn, err := s.conns.ConnForTekton(ctx, app.OrganizationID, app.RunnerClusterID)
	if err != nil {
		return ""
	}
	run, err := tekton.GetRun(ctx, conn, helm.RunNamespace, runName)
	if err != nil || run.PodName == "" {
		return ""
	}
	rc, err := k8s.StreamPodLogs(ctx, conn, helm.RunNamespace, run.PodName, k8s.LogOptions{Follow: false})
	if err != nil {
		return ""
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		return ""
	}
	return string(b)
}

// The rollout worker drives the service through the helm.Store seam, and the
// preview worker through helm.PreviewStore.
var (
	_ helm.Store        = (*Service)(nil)
	_ helm.PreviewStore = (*Service)(nil)
)

// ValidationError is a client-input error (bad/missing fields, an invalid
// cluster pairing) the handler maps to 400.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

// IsValidation reports whether err is a ValidationError.
func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

func validationErr(format string, args ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// CreateParams describes an application to register.
type CreateParams struct {
	Name            string
	ChartSource     string
	Config          map[string]string
	Values          string
	ReleaseName     string
	TargetNamespace string
	TargetClusterID uuid.UUID
	RunnerClusterID uuid.UUID
	// ValuesSources optionally pulls values files from git (orthogonal to the chart
	// source): an ordered list, each {repo_url, git_ref?, path}, layered in order
	// beneath the inline Values. Each needs repo_url + path (validated).
	ValuesSources []map[string]string
	// ChartCredentialID optionally attaches a private-chart credential. Nil means
	// public chart. Its type must be compatible with ChartSource (validated).
	ChartCredentialID *uuid.UUID
	// GitHubInstallationID optionally attaches a GitHub App installation for a
	// private github.com repo — the chart and/or a values source. Nil means public;
	// valid only when the chart or a values source is git (validated).
	GitHubInstallationID *uuid.UUID
}

// UpdateParams describes a change to an application. A nil field is unchanged.
// The clusters and chart source are fixed at registration; the chart coordinates
// (config), values, release name, target namespace, and name can change.
type UpdateParams struct {
	Name            *string
	Config          *map[string]string
	Values          *string
	ReleaseName     *string
	TargetNamespace *string
	// ValuesSources replaces the git values sources. Nil leaves them unchanged; a
	// non-nil (possibly empty) slice sets them — an empty slice clears all sources.
	ValuesSources *[]map[string]string
	// ChartCredentialID changes the attached credential. Nil leaves it unchanged;
	// a non-nil uuid.Nil detaches it; any other value attaches that credential
	// (validated against the app's chart source).
	ChartCredentialID *uuid.UUID
	// GitHubInstallationID changes the attached GitHub App installation. Nil
	// leaves it unchanged; a non-nil uuid.Nil detaches it; any other value
	// attaches that installation (only valid for a git chart source).
	GitHubInstallationID *uuid.UUID
}

// List returns the organization's applications, oldest first.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]*ent.Application, error) {
	return s.ent.Application.Query().
		Where(application.OrganizationID(orgID)).
		Order(ent.Asc(application.FieldCreatedAt)).
		All(ctx)
}

// Get returns one application scoped to the organization, or ent's NotFoundError.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (*ent.Application, error) {
	return s.ent.Application.Query().
		Where(application.OrganizationID(orgID), application.ID(id)).
		Only(ctx)
}

// Create validates the cluster pairing and chart-source fields, then registers
// the application in the pending state (no rollout is started here).
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, p CreateParams) (*ent.Application, error) {
	if err := s.validate(ctx, orgID, p); err != nil {
		return nil, err
	}
	create := s.ent.Application.Create().
		SetOrganizationID(orgID).
		SetName(p.Name).
		SetChartSource(application.ChartSource(p.ChartSource)).
		SetConfig(nonNilConfig(p.Config)).
		SetValues(p.Values).
		SetValuesSources(nonNilSources(p.ValuesSources)).
		SetReleaseName(p.ReleaseName).
		SetTargetNamespace(p.TargetNamespace).
		SetTargetClusterID(p.TargetClusterID).
		SetRunnerClusterID(p.RunnerClusterID)
	if p.ChartCredentialID != nil && *p.ChartCredentialID != uuid.Nil {
		create.SetChartCredentialID(*p.ChartCredentialID)
	}
	if p.GitHubInstallationID != nil && *p.GitHubInstallationID != uuid.Nil {
		create.SetGithubInstallationID(*p.GitHubInstallationID)
	}
	return create.Save(ctx)
}

// Update changes mutable fields of an application scoped to the organization,
// re-validating the chart-source fields if config changes.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, p UpdateParams) (*ent.Application, error) {
	app, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	upd := app.Update()
	if p.Name != nil {
		upd.SetName(*p.Name)
	}
	if p.Values != nil {
		upd.SetValues(*p.Values)
	}
	if p.ReleaseName != nil {
		upd.SetReleaseName(*p.ReleaseName)
	}
	if p.TargetNamespace != nil {
		if *p.TargetNamespace == "" {
			return nil, validationErr("target namespace cannot be empty")
		}
		upd.SetTargetNamespace(*p.TargetNamespace)
	}
	if p.Config != nil {
		cfg := nonNilConfig(*p.Config)
		if err := validateSourceConfig(app.ChartSource.String(), cfg); err != nil {
			return nil, err
		}
		upd.SetConfig(cfg)
	}
	if p.ValuesSources != nil {
		sources := nonNilSources(*p.ValuesSources)
		if err := validateValuesSources(sources); err != nil {
			return nil, err
		}
		upd.SetValuesSources(sources)
	}
	if p.ChartCredentialID != nil {
		if *p.ChartCredentialID == uuid.Nil {
			upd.ClearChartCredentialID()
		} else {
			if err := s.validateCredential(ctx, orgID, app.ChartSource.String(), *p.ChartCredentialID); err != nil {
				return nil, err
			}
			upd.SetChartCredentialID(*p.ChartCredentialID)
		}
	}
	if p.GitHubInstallationID != nil {
		if *p.GitHubInstallationID == uuid.Nil {
			upd.ClearGithubInstallationID()
		} else {
			// Validate against the values sources as they will be after this update
			// (the incoming list when it's changing, the stored one otherwise), so a
			// values-from-git source set in the same call is taken into account.
			effectiveSources := app.ValuesSources
			if p.ValuesSources != nil {
				effectiveSources = *p.ValuesSources
			}
			if err := s.validateInstallation(ctx, orgID, app.ChartSource.String(), effectiveSources, *p.GitHubInstallationID); err != nil {
				return nil, err
			}
			upd.SetGithubInstallationID(*p.GitHubInstallationID)
		}
	}
	return upd.Save(ctx)
}

// Delete removes an application scoped to the organization.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	app, err := s.Get(ctx, orgID, id)
	if err != nil {
		return err
	}
	return s.ent.Application.DeleteOne(app).Exec(ctx)
}

// BeginRollout moves an application into the in-flight state for the given
// action so the handler can enqueue the rollout job. deploy/upgrade go to
// deploying; uninstall to uninstalling. Returns a ValidationError for an unknown
// action.
func (s *Service) BeginRollout(ctx context.Context, orgID, id uuid.UUID, action string) (*ent.Application, error) {
	app, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	var status application.Status
	msg := "queued for rollout"
	switch action {
	case helm.ActionDeploy, helm.ActionUpgrade:
		status = application.StatusDeploying
	case helm.ActionUninstall:
		status = application.StatusUninstalling
		msg = "queued for uninstall"
	default:
		return nil, validationErr("unknown rollout action %q", action)
	}
	return app.Update().SetStatus(status).SetStatusMessage(msg).Save(ctx)
}

// BeginPreview moves an application into the refreshing state so the handler can
// enqueue the preview job. It refuses while a rollout is in flight (deploying /
// uninstalling) — diffing against a cluster mid-change would be meaningless — and
// returns that as a ValidationError the handler maps to 409.
func (s *Service) BeginPreview(ctx context.Context, orgID, id uuid.UUID) (*ent.Application, error) {
	app, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if app.Status == application.StatusDeploying || app.Status == application.StatusUninstalling {
		return nil, validationErr("a rollout is in progress; refresh once it settles")
	}
	return app.Update().
		SetSyncStatus(application.SyncStatusRefreshing).
		SetSyncMessage("queued for refresh").
		SetSyncRunName("").
		Save(ctx)
}

// MarkPreview persists a non-terminal sync transition reported by the preview
// worker (it satisfies helm.PreviewStore): status is one of the helm.Sync*
// values (refreshing / error). jobID/runName are set only when non-empty; the
// message is always set. It writes only the sync_* columns, never the rollout
// lifecycle.
func (s *Service) MarkPreview(ctx context.Context, orgID, appID uuid.UUID, jobID, status, message, runName string) error {
	st, err := syncStatusEnum(status)
	if err != nil {
		return err
	}
	upd := s.ent.Application.Update().
		Where(application.OrganizationID(orgID), application.ID(appID)).
		SetSyncStatus(st).
		SetSyncMessage(message)
	if jobID != "" {
		upd.SetSyncJobID(jobID)
	}
	if runName != "" {
		upd.SetSyncRunName(runName)
	}
	_, err = upd.Save(ctx)
	return err
}

// maxDiffBytes caps the stored diff so a pathological all-additions diff (e.g. a
// huge chart's initial install) can't bloat the row or the API response.
const maxDiffBytes = 256 * 1024

// CompletePreview records a successful diff run (it satisfies helm.PreviewStore):
// it captures the run's logs, parses the diff body + verdict + resolved
// revisions, and sets sync_status to synced or out_of_sync. The worker can't make
// that call itself — the verdict is in the diff output, not the run phase. A
// failure to capture logs is recorded as an error status (the run succeeded but
// we have nothing to show).
func (s *Service) CompletePreview(ctx context.Context, orgID, appID uuid.UUID, jobID, runName string) error {
	app, err := s.Get(ctx, orgID, appID)
	if err != nil {
		return err
	}
	logs := ""
	if s.captureLogs != nil {
		logs = s.captureLogs(ctx, app, runName)
	}
	diff := helm.ParseDiff(logs)
	rev := helm.ParseRevisions(logs)

	status := application.SyncStatusSynced
	msg := "in sync"
	if diff.HasChanges {
		status = application.SyncStatusOutOfSync
		msg = "out of sync"
	}
	body := diff.Body
	if len(body) > maxDiffBytes {
		body = body[:maxDiffBytes] + "\n... diff truncated ..."
	}
	upd := app.Update().
		SetSyncStatus(status).
		SetSyncMessage(msg).
		SetLastDiff(body).
		SetDesiredChartRevision(rev.Chart).
		SetDesiredValuesRevision(valuesRevision(app.ValuesSources, rev.Values)).
		SetLastRefreshedAt(time.Now())
	if jobID != "" {
		upd.SetSyncJobID(jobID)
	}
	if runName != "" {
		upd.SetSyncRunName(runName)
	}
	_, err = upd.Save(ctx)
	return err
}

// MarkRollout persists a rollout-lifecycle transition reported by the worker (it
// satisfies helm.Store). status must be one of the helm.Status* values;
// jobID/runName are set only when non-empty, message is always set (pass "" to
// clear). The org id from the job args still flows through the scoped query.
func (s *Service) MarkRollout(ctx context.Context, orgID, appID uuid.UUID, jobID, status, message, runName string) error {
	app, err := s.Get(ctx, orgID, appID)
	if err != nil {
		return err
	}
	st, err := rolloutStatusEnum(status)
	if err != nil {
		return err
	}
	upd := s.ent.Application.Update().
		Where(application.OrganizationID(orgID), application.ID(appID)).
		SetStatus(st).
		SetStatusMessage(message)
	if jobID != "" {
		upd.SetJobID(jobID)
	}
	if runName != "" {
		upd.SetLastRunName(runName)
	}
	if _, err := upd.Save(ctx); err != nil {
		return err
	}
	// Mirror the transition onto this rollout's history record (created by the
	// API at enqueue time, found by job id). Best-effort: the application row is
	// the source of truth for the live UI, so a history-write or log-capture
	// failure must not fail the rollout job (the worker retries on a non-nil
	// return). A missing record (pre-feature rows, DB-less route tests) is a no-op.
	s.markDeployment(ctx, app, jobID, st, message, runName)
	return nil
}

// markDeployment folds a rollout transition into the matching deployment row and,
// once the run is terminal, stamps finished_at and captures the run's logs.
func (s *Service) markDeployment(ctx context.Context, app *ent.Application, jobID string, appStatus application.Status, message, runName string) {
	if jobID == "" {
		return
	}
	dep, err := s.ent.Deployment.Query().
		Where(
			deployment.OrganizationID(app.OrganizationID),
			deployment.ApplicationID(app.ID),
			deployment.JobID(jobID),
		).
		Only(ctx)
	if err != nil {
		return
	}
	runStatus, terminal := deploymentStatusFor(appStatus)
	upd := dep.Update().SetStatus(runStatus).SetMessage(message)
	if runName != "" {
		upd.SetRunName(runName)
	}
	if terminal {
		upd.SetFinishedAt(time.Now())
		if s.captureLogs != nil {
			if logs := s.captureLogs(ctx, app, runName); logs != "" {
				upd.SetLogs(logs)
				// Record the git commit SHAs this run resolved (echoed into the logs by
				// the rollout script). Empty — a non-git source — leaves the column at
				// its default, so a redeploy with a changed source clears it.
				rev := helm.ParseRevisions(logs)
				upd.SetChartRevision(rev.Chart)
				upd.SetValuesRevision(valuesRevision(app.ValuesSources, rev.Values))
			}
		}
	}
	_, _ = upd.Save(ctx)
}

// RecordDeployment creates the history record for a rollout the API is about to
// enqueue, in the running state, keyed by the River job id so the worker's
// MarkRollout transitions find it. Returns a ValidationError for an unknown action.
func (s *Service) RecordDeployment(ctx context.Context, orgID, appID uuid.UUID, action, jobID string) (*ent.Deployment, error) {
	act, err := deploymentActionEnum(action)
	if err != nil {
		return nil, err
	}
	return s.ent.Deployment.Create().
		SetOrganizationID(orgID).
		SetApplicationID(appID).
		SetAction(act).
		SetJobID(jobID).
		Save(ctx)
}

// ListDeployments returns an application's rollout runs newest-first, scoped to
// the organization. Capped — the history is for recent runs, not an archive.
func (s *Service) ListDeployments(ctx context.Context, orgID, appID uuid.UUID) ([]*ent.Deployment, error) {
	return s.ent.Deployment.Query().
		Where(deployment.OrganizationID(orgID), deployment.ApplicationID(appID)).
		Order(ent.Desc(deployment.FieldCreatedAt)).
		Limit(100).
		All(ctx)
}

// GetDeployment returns one rollout run scoped to the organization and
// application, or ent's NotFoundError.
func (s *Service) GetDeployment(ctx context.Context, orgID, appID, id uuid.UUID) (*ent.Deployment, error) {
	return s.ent.Deployment.Query().
		Where(
			deployment.OrganizationID(orgID),
			deployment.ApplicationID(appID),
			deployment.ID(id),
		).
		Only(ctx)
}

// runInputs is the shared resolution both a rollout and a preview need: the app,
// the runner connection, the target's connection method (for the wait timeout),
// the injected Files (kubeconfig + values + any credentials), and the auth flags.
// Only the final helm verb (Rollout.Action) differs between the two callers.
type runInputs struct {
	app           *ent.Application
	runnerConn    k8s.Connection
	targetMethod  k8s.Method
	files         map[string]string
	hasCredential bool
	hasGitToken   bool
}

// resolveRunInputs resolves the runner connection, builds the target cluster's
// kubeconfig (minting any cloud token late, this attempt), decrypts any chart
// credential, mints any GitHub App token, and assembles the injected Files.
// pullsChart is false only for uninstall (which pulls nothing), skipping the
// chart credential + git token.
func (s *Service) resolveRunInputs(ctx context.Context, orgID, appID uuid.UUID, pullsChart bool) (runInputs, error) {
	app, err := s.Get(ctx, orgID, appID)
	if err != nil {
		return runInputs{}, err
	}
	runnerConn, err := s.conns.ConnForTekton(ctx, orgID, app.RunnerClusterID)
	if err != nil {
		return runInputs{}, err
	}
	targetConn, err := s.conns.ConnForTekton(ctx, orgID, app.TargetClusterID)
	if err != nil {
		return runInputs{}, err
	}
	// When the runner is the target cluster, the Helm job runs inside the cluster
	// it deploys to, so the injected kubeconfig must use the in-cluster API server
	// address rather than the registered (possibly host-only) endpoint.
	sameCluster := app.RunnerClusterID == app.TargetClusterID
	kubeconfig, err := k8s.Kubeconfig(ctx, targetConn, sameCluster)
	if err != nil {
		return runInputs{}, err
	}

	in := runInputs{
		app:          app,
		runnerConn:   runnerConn,
		targetMethod: targetConn.Method,
		files: map[string]string{
			helm.KubeconfigFile: string(kubeconfig),
			helm.ValuesFile:     app.Values,
		},
	}

	// Attach a private-chart credential, when one is set: resolve (decrypt) it and
	// inject the username/password as mounted files. The script reads them at
	// runtime, so the password never lands in the script string, the TaskRun
	// manifest, or env — only in the same owner-referenced, GC'd Secret as the
	// kubeconfig. Uninstall pulls no chart, so it needs no credential.
	if app.ChartCredentialID != uuid.Nil && pullsChart {
		if s.creds == nil {
			return runInputs{}, fmt.Errorf("applications: app references a chart credential but the chart-credentials service is not configured")
		}
		cred, err := s.creds.Resolve(ctx, orgID, app.ChartCredentialID)
		if err != nil {
			return runInputs{}, err
		}
		in.files[helm.RegistryUsernameFile] = cred.Username
		in.files[helm.RegistryPasswordFile] = cred.Password
		in.hasCredential = true
	}

	// Attach a GitHub App installation token, when one is set, for a private-Git
	// chart: mint it late (this attempt) so River retries always carry a fresh
	// token, and inject it as the mounted git-credentials file. The script wires
	// git's credential helper to it, so the token never lands in the script
	// string, the clone's argv, or the workspace .git/config. Uninstall pulls no
	// chart, so it needs no token.
	if app.GithubInstallationID != uuid.Nil && pullsChart {
		if s.gitTokens == nil {
			return runInputs{}, fmt.Errorf("applications: app references a github installation but the github-installations service is not configured")
		}
		token, err := s.gitTokens.InstallationToken(ctx, orgID, app.GithubInstallationID)
		if err != nil {
			return runInputs{}, err
		}
		in.files[helm.GitCredentialsFile] = "https://x-access-token:" + token + "@github.com"
		in.hasGitToken = true
	}
	return in, nil
}

// ResolveRollout satisfies helm.Store: it resolves the shared run inputs and
// assembles the RunSpec (script + injected Files) for the action. The wait
// timeout is tiered by the target's connection method (its credential longevity).
func (s *Service) ResolveRollout(ctx context.Context, orgID, appID uuid.UUID, action string) (helm.RolloutPlan, error) {
	in, err := s.resolveRunInputs(ctx, orgID, appID, action != helm.ActionUninstall)
	if err != nil {
		return helm.RolloutPlan{}, err
	}
	script := helm.Script(helm.Rollout{
		Action:          action,
		ChartSource:     in.app.ChartSource.String(),
		Config:          in.app.Config,
		ValuesSources:   in.app.ValuesSources,
		ReleaseName:     releaseName(in.app),
		TargetNamespace: in.app.TargetNamespace,
		WaitTimeout:     helm.WaitTimeout(in.targetMethod),
		HasCredential:   in.hasCredential,
		HasGitToken:     in.hasGitToken,
	})
	return helm.RolloutPlan{
		RunnerConn:  in.runnerConn,
		ExistingRun: in.app.LastRunName,
		RunSpec: tekton.RunSpec{
			Name:   runPrefix(in.app),
			Image:  helm.DefaultImage,
			Script: script,
			Files:  in.files,
		},
	}, nil
}

// ResolvePreview satisfies helm.PreviewStore: same resolution as ResolveRollout,
// but it renders the `helm diff` (ActionPreview) script and re-attaches to the
// app's preview run (sync_run_name), never the last rollout run.
func (s *Service) ResolvePreview(ctx context.Context, orgID, appID uuid.UUID) (helm.PreviewPlan, error) {
	in, err := s.resolveRunInputs(ctx, orgID, appID, true)
	if err != nil {
		return helm.PreviewPlan{}, err
	}
	script := helm.Script(helm.Rollout{
		Action:          helm.ActionPreview,
		ChartSource:     in.app.ChartSource.String(),
		Config:          in.app.Config,
		ValuesSources:   in.app.ValuesSources,
		ReleaseName:     releaseName(in.app),
		TargetNamespace: in.app.TargetNamespace,
		WaitTimeout:     helm.WaitTimeout(in.targetMethod),
		HasCredential:   in.hasCredential,
		HasGitToken:     in.hasGitToken,
	})
	return helm.PreviewPlan{
		RunnerConn:  in.runnerConn,
		ExistingRun: in.app.SyncRunName,
		RunSpec: tekton.RunSpec{
			Name:   runPrefix(in.app),
			Image:  helm.DefaultImage,
			Script: script,
			Files:  in.files,
		},
	}, nil
}

// validate checks the cluster pairing and chart-source fields for a create.
func (s *Service) validate(ctx context.Context, orgID uuid.UUID, p CreateParams) error {
	if p.TargetNamespace == "" {
		return validationErr("target namespace is required")
	}
	target, err := s.ent.Cluster.Query().
		Where(cluster.OrganizationID(orgID), cluster.ID(p.TargetClusterID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return validationErr("target cluster not found in this organization")
		}
		return err
	}
	runner, err := s.ent.Cluster.Query().
		Where(cluster.OrganizationID(orgID), cluster.ID(p.RunnerClusterID)).
		WithTekton().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return validationErr("runner cluster not found in this organization")
		}
		return err
	}

	// In-cluster targets are only reachable from a pod in that same cluster, so
	// the runner must be that cluster. Use the token method for a remote runner.
	if target.ConnectionMethod == cluster.ConnectionMethodInCluster && runner.ID != target.ID {
		return validationErr("an in-cluster target requires the runner to be that same cluster; to use a different runner, register the target via the token method with an external endpoint")
	}
	// The runner must be designated to run jobs (Tekton enabled).
	if runner.Edges.Tekton == nil || !runner.Edges.Tekton.Enabled {
		return validationErr("runner cluster is not configured to run jobs (enable Tekton on it first)")
	}
	if err := validateSourceConfig(p.ChartSource, p.Config); err != nil {
		return err
	}
	if err := validateValuesSources(p.ValuesSources); err != nil {
		return err
	}
	if p.ChartCredentialID != nil && *p.ChartCredentialID != uuid.Nil {
		if err := s.validateCredential(ctx, orgID, p.ChartSource, *p.ChartCredentialID); err != nil {
			return err
		}
	}
	if p.GitHubInstallationID != nil && *p.GitHubInstallationID != uuid.Nil {
		if err := s.validateInstallation(ctx, orgID, p.ChartSource, p.ValuesSources, *p.GitHubInstallationID); err != nil {
			return err
		}
	}
	return nil
}

// validateInstallation confirms the GitHub App installation exists in the
// organization and that the app actually pulls from git — either a git chart
// source or a git-sourced values file (the installation authenticates a private
// github.com clone of either). The scoping query is the security boundary; the
// source/values check prevents attaching an installation to an app where it
// would do nothing.
func (s *Service) validateInstallation(ctx context.Context, orgID uuid.UUID, source string, valuesSources []map[string]string, installID uuid.UUID) error {
	if source != helm.SourceGit && !valuesFromGit(valuesSources) {
		return validationErr("a github installation can only be attached to an app that pulls from git (a git chart source or git-sourced values)")
	}
	exists, err := s.ent.GitHubInstallation.Query().
		Where(githubinstallation.OrganizationID(orgID), githubinstallation.ID(installID)).
		Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return validationErr("github installation not found in this organization")
	}
	return nil
}

// validateCredential confirms the chart source supports a credential and the
// credential exists in the organization (the scoping query is the security
// boundary). A chart credential is a basic-auth username/password pair that
// works for both http_repo and oci charts; git charts take none (they
// authenticate via a GitHub App, not a static credential).
func (s *Service) validateCredential(ctx context.Context, orgID uuid.UUID, source string, credID uuid.UUID) error {
	if !sourceSupportsCredential(source) {
		return validationErr("chart source %q does not support credentials", source)
	}
	exists, err := s.ent.ChartCredential.Query().
		Where(chartcredential.OrganizationID(orgID), chartcredential.ID(credID)).
		Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return validationErr("chart credential not found in this organization")
	}
	return nil
}

// sourceSupportsCredential reports whether a chart source pulls from a registry
// that takes a username/password credential. git charts take none (use a GitHub
// App).
func sourceSupportsCredential(source string) bool {
	return source == helm.SourceHTTPRepo || source == helm.SourceOCI
}

// validateSourceConfig checks the per-source required chart fields are present.
func validateSourceConfig(source string, cfg map[string]string) error {
	switch source {
	case helm.SourceHTTPRepo:
		return requireFields(cfg, helm.ConfigRepoURL, helm.ConfigChart)
	case helm.SourceOCI:
		return requireFields(cfg, helm.ConfigRepoURL)
	case helm.SourceGit:
		return requireFields(cfg, helm.ConfigRepoURL)
	default:
		return validationErr("unknown chart source %q", source)
	}
}

// validateValuesSources checks each git values source carries a repo URL and a
// file path (the ref is optional — default branch). The list order is the helm
// -f layering order, so an empty list is valid (inline-only values).
func validateValuesSources(sources []map[string]string) error {
	for i, src := range sources {
		if src[helm.ValuesSourceRepoURL] == "" {
			return validationErr("values source %d: %q is required", i+1, helm.ValuesSourceRepoURL)
		}
		if src[helm.ValuesSourcePath] == "" {
			return validationErr("values source %d: %q is required", i+1, helm.ValuesSourcePath)
		}
	}
	return nil
}

// valuesFromGit reports whether any values source pulls from a git repo.
func valuesFromGit(sources []map[string]string) bool {
	for _, src := range sources {
		if src[helm.ValuesSourceRepoURL] != "" {
			return true
		}
	}
	return false
}

// valuesRevision pairs each values source with the commit SHA it resolved to
// (parsed from the run logs, keyed by source index) as newline-joined
// "<repo>@<sha>" lines — the durable record of what a pull-on-deploy run used.
// Sources with no captured SHA (e.g. a failed clone) are skipped.
func valuesRevision(sources []map[string]string, shas map[int]string) string {
	var lines []string
	for i, src := range sources {
		if sha := shas[i]; sha != "" {
			lines = append(lines, src[helm.ValuesSourceRepoURL]+"@"+sha)
		}
	}
	return strings.Join(lines, "\n")
}

func requireFields(cfg map[string]string, keys ...string) error {
	for _, k := range keys {
		if cfg[k] == "" {
			return validationErr("chart source field %q is required", k)
		}
	}
	return nil
}

// releaseName is the Helm release name: the explicit release_name, or the app
// name when unset.
func releaseName(app *ent.Application) string {
	if app.ReleaseName != "" {
		return app.ReleaseName
	}
	return app.Name
}

// runPrefix is the TaskRun generateName prefix (a DNS-1123 label). It is derived
// from the release name; lib/tekton appends a unique suffix.
func runPrefix(app *ent.Application) string {
	return "helm-" + sanitizeLabel(releaseName(app))
}

// sanitizeLabel lowercases s and replaces any non-DNS-1123-label character with
// a hyphen, trimming leading/trailing hyphens, so it's a valid generateName
// component. An empty result falls back to "release".
func sanitizeLabel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "release"
	}
	return out
}

// rolloutStatusEnum validates and converts a helm.Status* string to the ent
// enum, rejecting unknown values so a typo can't reach the column.
func rolloutStatusEnum(s string) (application.Status, error) {
	st := application.Status(s)
	switch st {
	case application.StatusPending,
		application.StatusDeploying,
		application.StatusDeployed,
		application.StatusFailed,
		application.StatusUninstalling,
		application.StatusUninstalled:
		return st, nil
	default:
		return "", fmt.Errorf("applications: unknown rollout status %q", s)
	}
}

// syncStatusEnum validates and converts a helm.Sync* string to the ent enum,
// rejecting unknown values so a typo can't reach the column.
func syncStatusEnum(s string) (application.SyncStatus, error) {
	st := application.SyncStatus(s)
	switch st {
	case application.SyncStatusUnknown,
		application.SyncStatusRefreshing,
		application.SyncStatusSynced,
		application.SyncStatusOutOfSync,
		application.SyncStatusError:
		return st, nil
	default:
		return "", fmt.Errorf("applications: unknown sync status %q", s)
	}
}

// deploymentStatusFor maps an application rollout status to the run-level
// deployment status, and reports whether the run has reached a terminal phase
// (deployed/uninstalled → succeeded, failed → failed; everything else running).
func deploymentStatusFor(appStatus application.Status) (deployment.Status, bool) {
	switch appStatus {
	case application.StatusDeployed, application.StatusUninstalled:
		return deployment.StatusSucceeded, true
	case application.StatusFailed:
		return deployment.StatusFailed, true
	default:
		return deployment.StatusRunning, false
	}
}

// deploymentActionEnum validates and converts a rollout action string to the ent
// enum, rejecting unknown values.
func deploymentActionEnum(action string) (deployment.Action, error) {
	switch action {
	case helm.ActionDeploy:
		return deployment.ActionDeploy, nil
	case helm.ActionUpgrade:
		return deployment.ActionUpgrade, nil
	case helm.ActionUninstall:
		return deployment.ActionUninstall, nil
	default:
		return "", validationErr("unknown rollout action %q", action)
	}
}

// nonNilConfig guards against a nil map reaching the JSON column.
func nonNilConfig(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// nonNilSources guards against a nil slice reaching the JSON column.
func nonNilSources(s []map[string]string) []map[string]string {
	if s == nil {
		return []map[string]string{}
	}
	return s
}
