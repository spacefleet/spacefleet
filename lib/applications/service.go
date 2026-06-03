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
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/application"
	"github.com/spacefleet/spacefleet/ent/cluster"
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

// Service is a thin wrapper over the ent client plus the connection resolver.
type Service struct {
	ent   *ent.Client
	conns ConnResolver
}

func NewService(entClient *ent.Client, conns ConnResolver) *Service {
	return &Service{ent: entClient, conns: conns}
}

// The rollout worker drives the service through the helm.Store seam.
var _ helm.Store = (*Service)(nil)

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
	return s.ent.Application.Create().
		SetOrganizationID(orgID).
		SetName(p.Name).
		SetChartSource(application.ChartSource(p.ChartSource)).
		SetConfig(nonNilConfig(p.Config)).
		SetValues(p.Values).
		SetReleaseName(p.ReleaseName).
		SetTargetNamespace(p.TargetNamespace).
		SetTargetClusterID(p.TargetClusterID).
		SetRunnerClusterID(p.RunnerClusterID).
		Save(ctx)
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

// MarkRollout persists a rollout-lifecycle transition reported by the worker (it
// satisfies helm.Store). status must be one of the helm.Status* values;
// jobID/runName are set only when non-empty, message is always set (pass "" to
// clear). The org id from the job args still flows through the scoped query.
func (s *Service) MarkRollout(ctx context.Context, orgID, appID uuid.UUID, jobID, status, message, runName string) error {
	if _, err := s.Get(ctx, orgID, appID); err != nil {
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
	_, err = upd.Save(ctx)
	return err
}

// ResolveRollout satisfies helm.Store: it resolves the runner connection, builds
// the target cluster's kubeconfig (minting any cloud token late, this attempt),
// and assembles the RunSpec (script + injected Files) for the action. The wait
// timeout is tiered by the target's connection method (its credential longevity).
func (s *Service) ResolveRollout(ctx context.Context, orgID, appID uuid.UUID, action string) (helm.RolloutPlan, error) {
	app, err := s.Get(ctx, orgID, appID)
	if err != nil {
		return helm.RolloutPlan{}, err
	}
	runnerConn, err := s.conns.ConnForTekton(ctx, orgID, app.RunnerClusterID)
	if err != nil {
		return helm.RolloutPlan{}, err
	}
	targetConn, err := s.conns.ConnForTekton(ctx, orgID, app.TargetClusterID)
	if err != nil {
		return helm.RolloutPlan{}, err
	}
	kubeconfig, err := k8s.Kubeconfig(ctx, targetConn)
	if err != nil {
		return helm.RolloutPlan{}, err
	}

	script := helm.Script(helm.Rollout{
		Action:          action,
		ChartSource:     app.ChartSource.String(),
		Config:          app.Config,
		ReleaseName:     releaseName(app),
		TargetNamespace: app.TargetNamespace,
		WaitTimeout:     helm.WaitTimeout(targetConn.Method),
	})

	return helm.RolloutPlan{
		RunnerConn:  runnerConn,
		ExistingRun: app.LastRunName,
		RunSpec: tekton.RunSpec{
			Name:   runPrefix(app),
			Image:  helm.DefaultImage,
			Script: script,
			Files: map[string]string{
				helm.KubeconfigFile: string(kubeconfig),
				helm.ValuesFile:     app.Values,
			},
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
	return validateSourceConfig(p.ChartSource, p.Config)
}

// validateSourceConfig checks the per-source required fields are present.
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

// nonNilConfig guards against a nil map reaching the JSON column.
func nonNilConfig(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
