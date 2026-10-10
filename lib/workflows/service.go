// Package workflows holds the application deploy-workflow use cases: an
// application owns ordered stages of typed Components (helm release, manifest
// apply, OpenTofu module), and a run of that workflow is a WorkflowRun with one
// ComponentRun per execution step. This package owns the workflow CRUD, the
// write-time validation, the run snapshot, and the execution the worker drives.
//
// It is a thin wrapper over the ent client. Like every org-scoped resource, every
// query is scoped by organization id — that scoping, not a handler's membership
// check, is the security boundary. A component carries no secrets of its own; the
// credentials it references (chart credential, GitHub installation) and the target
// cluster are validated and resolved elsewhere, the same way lib/applications does.
package workflows

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/application"
	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/ent/component"
	"github.com/spacefleet/spacefleet/ent/tektoninstallation"
	"github.com/spacefleet/spacefleet/ent/variable"
	"github.com/spacefleet/spacefleet/ent/workflowstage"
	"github.com/spacefleet/spacefleet/lib/tekton"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// Service is a thin wrapper over the ent client.
type Service struct {
	ent *ent.Client
	// reapHook, when set, is called for each run the reaper settles — after the
	// run and its steps are marked failed — so the worker process can release
	// what the abandoned run left on its runner cluster (planfile-handover
	// Secrets). See OnReaped.
	reapHook func(context.Context, *ent.WorkflowRun)
	// checks posts check runs for pull-request previews; externalURL is the
	// deployment's public base URL those checks link to. See SetGitHubChecks.
	checks      CheckRunClient
	externalURL string
	// eventHook receives run events (see OnEvent); nil drops them.
	eventHook func(context.Context, Event)
	// managedState reports whether the Spacefleet state backend can be used
	// (the deployment has a secret key). See SetManagedState.
	managedState bool
}

// SetManagedState records whether managed OpenTofu state is available —
// it needs SPACEFLEET_SECRET_KEY to seal state and sign runner tokens. When
// it isn't, saving a workflow with a component on the spacefleet backend
// fails with a clear message instead of failing every run later.
func (s *Service) SetManagedState(enabled bool) {
	s.managedState = enabled
}

// NewService builds the workflow service over the ent client.
func NewService(entClient *ent.Client) *Service {
	return &Service{ent: entClient}
}

// OnReaped registers fn to run for every run ReapStuckRuns settles. Only the
// worker process registers one (it owns the cluster connections); the service
// itself never touches a cluster. fn runs synchronously inside the sweep and
// must be best-effort — a reaped run is already settled by the time it is
// called, so nothing fn does can change the run's outcome.
func (s *Service) OnReaped(fn func(context.Context, *ent.WorkflowRun)) {
	s.reapHook = fn
}

// ComponentInput is one component of a proposed workflow, as supplied by the
// builder. ID is client-provided (the builder assigns a stable uuid per
// component) so identity — and with it the component's variables, recorded
// state, and run history — survives a replace. Config is the type-specific,
// non-secret param map, validated per type. The stage it runs in is the
// StageInput holding it.
type ComponentInput struct {
	ID                uuid.UUID
	Name              string
	Type              string
	Config            map[string]string
	ContinueOnFailure bool
	RequiresApproval  bool
	// ApprovalPolicy applies at the node's approval gate; the zero value is
	// the default policy. Normalised by ReplaceWorkflow (see
	// normalizeApprovalPolicy).
	ApprovalPolicy       ApprovalPolicy
	TargetClusterID      *uuid.UUID
	TargetNamespace      string
	ChartCredentialID    *uuid.UUID
	GitHubInstallationID *uuid.UUID
}

// GetWorkflow returns an application's workflow — its stages in run order, each
// with its components (Edges.Components) in display order — scoped to the
// organization. It first confirms the application belongs to the org (so a
// caller can't read another org's workflow by id); a missing application
// surfaces as ent's NotFoundError.
func (s *Service) GetWorkflow(ctx context.Context, orgID, appID uuid.UUID) ([]*ent.WorkflowStage, error) {
	if _, err := s.getApp(ctx, orgID, appID); err != nil {
		return nil, err
	}
	return s.loadStages(ctx, orgID, appID)
}

// loadStages is the unguarded org-scoped workflow query, shared by GetWorkflow
// (which adds the application-membership check), ReplaceWorkflow, and the run
// snapshot (which already confirmed the app). Components are filtered by the
// org too, so the eager load can't cross the tenancy boundary.
func (s *Service) loadStages(ctx context.Context, orgID, appID uuid.UUID) ([]*ent.WorkflowStage, error) {
	return s.ent.WorkflowStage.Query().
		Where(workflowstage.OrganizationID(orgID), workflowstage.ApplicationID(appID)).
		Order(ent.Asc(workflowstage.FieldOrdinal)).
		WithComponents(func(q *ent.ComponentQuery) {
			q.Where(component.OrganizationID(orgID)).
				Order(ent.Asc(component.FieldOrdinal), ent.Asc(component.FieldCreatedAt))
		}).
		All(ctx)
}

// ReplaceWorkflow validates the proposed workflow (stages, each with its
// components) and atomically replaces the application's workflow with it: in
// one transaction it deletes the app's existing components and stages and
// recreates them, preserving each input's client-provided id (so a component's
// variables, state, and history stay attached across an edit) and recording
// the given order as each stage's and component's ordinal. A validation
// failure (see validateWorkflow) is returned before any write. The application
// must belong to the organization.
//
// Returns the persisted workflow, reloaded outside the transaction.
func (s *Service) ReplaceWorkflow(ctx context.Context, orgID, appID uuid.UUID, stages []StageInput) ([]*ent.WorkflowStage, error) {
	return s.ReplaceWorkflowWith(ctx, orgID, appID, stages, ReplaceOptions{})
}

// ReplaceOptions adjusts a workflow save.
type ReplaceOptions struct {
	// AllowBackendChange lets an OpenTofu component that still manages
	// resources move to a different state backend (see checkBackendChanges)
	// — the caller confirmed that the next run starts from empty state.
	AllowBackendChange bool
	// AllowStateDeletion lets a save remove an OpenTofu component on the
	// managed backend whose state still lists resources (see
	// checkStateDeletions) — the caller confirmed that the resources keep
	// running and the state is deleted.
	AllowStateDeletion bool
}

// ReplaceWorkflowWith is ReplaceWorkflow with options.
func (s *Service) ReplaceWorkflowWith(ctx context.Context, orgID, appID uuid.UUID, stages []StageInput, opts ReplaceOptions) ([]*ent.WorkflowStage, error) {
	app, err := s.getApp(ctx, orgID, appID)
	if err != nil {
		return nil, err
	}
	// Normalise each component's approval policy (trim/lowercase/dedupe the
	// approvers, bound the counts) before the validation, so what is persisted
	// is canonical and a bad policy is a 400 like any config error.
	for i := range stages {
		for j := range stages[i].Components {
			p, err := normalizeApprovalPolicy(stages[i].Components[j])
			if err != nil {
				return nil, err
			}
			stages[i].Components[j].ApprovalPolicy = p
		}
	}
	if err := validateWorkflow(stages); err != nil {
		return nil, err
	}
	nodes := flattenStages(stages)
	if err := s.validateComponentTargets(ctx, orgID, app, nodes); err != nil {
		return nil, err
	}
	if err := s.checkMovedComponents(ctx, orgID, appID, nodes); err != nil {
		return nil, err
	}
	if err := s.resolveInstallations(ctx, orgID, stages); err != nil {
		return nil, err
	}
	if !s.managedState {
		for _, n := range nodes {
			if n.Type == TypeTerraform && n.Config[terraformConfigBackend] == tofu.BackendSpacefleet {
				return nil, fmt.Errorf("%w: node %q (terraform) the %s state backend needs SPACEFLEET_SECRET_KEY, which this Spacefleet does not have; ask the operator to set it, or use a cloud backend", ErrInvalidConfig, n.Name, tofu.BackendSpacefleet)
			}
		}
	}
	if !opts.AllowBackendChange {
		if err := s.checkBackendChanges(ctx, orgID, appID, nodes); err != nil {
			return nil, err
		}
	}
	stateDel, err := s.checkStateDeletions(ctx, orgID, appID, nodes, opts.AllowStateDeletion)
	if err != nil {
		return nil, err
	}

	tx, err := s.ent.Tx(ctx)
	if err != nil {
		return nil, err
	}

	// Delete components first (they FK stage_id → workflow_stages), then stages.
	if _, err := tx.Component.Delete().
		Where(component.OrganizationID(orgID), component.ApplicationID(appID)).
		Exec(ctx); err != nil {
		return nil, rollback(tx, err)
	}
	if _, err := tx.WorkflowStage.Delete().
		Where(workflowstage.OrganizationID(orgID), workflowstage.ApplicationID(appID)).
		Exec(ctx); err != nil {
		return nil, rollback(tx, err)
	}

	// Recreate each stage before its components, since a component's stage_id
	// references a stage row.
	for i, st := range stages {
		if err := tx.WorkflowStage.Create().
			SetID(st.ID).
			SetOrganizationID(orgID).
			SetApplicationID(appID).
			SetName(strings.TrimSpace(st.Name)).
			SetOrdinal(i).
			Exec(ctx); err != nil {
			return nil, rollback(tx, err)
		}
		for j, n := range st.Components {
			if err := s.createComponent(ctx, tx, orgID, appID, st.ID, j, n); err != nil {
				return nil, rollback(tx, err)
			}
		}
	}

	// Reconcile component-scoped variables: drop those whose component is no
	// longer in the workflow. Component variables aren't FK'd to components (the
	// components are deleted + recreated above with stable ids), so a variable
	// whose component_id is absent from the new component set belongs to a
	// removed component and must be cleaned up. Components that persist keep
	// their id, so their variables survive untouched.
	del := tx.Variable.Delete().Where(
		variable.OrganizationID(orgID),
		variable.ApplicationID(appID),
		variable.ComponentIDNotNil(),
	)
	if len(nodes) > 0 {
		keep := make([]uuid.UUID, len(nodes))
		for i, n := range nodes {
			keep[i] = n.ID
		}
		del = del.Where(variable.ComponentIDNotIn(keep...))
	}
	if _, err := del.Exec(ctx); err != nil {
		return nil, rollback(tx, err)
	}
	// A removed OpenTofu component's managed state goes with it: keyed by
	// the component's id, nothing could reach it again.
	if err := deleteStates(ctx, tx, orgID, appID, stateDel); err != nil {
		return nil, rollback(tx, err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// Reload outside the transaction so callers see the persisted rows in order.
	return s.loadStages(ctx, orgID, appID)
}

// createComponent persists one validated component within the replace
// transaction, keeping its client-provided id and placing it in its stage at
// the given display ordinal. Optional FK fields (target cluster, credential,
// installation) are set only when present and non-zero, mirroring
// lib/applications.
func (s *Service) createComponent(ctx context.Context, tx *ent.Tx, orgID, appID, stageID uuid.UUID, ordinal int, n ComponentInput) error {
	create := tx.Component.Create().
		SetID(n.ID).
		SetOrganizationID(orgID).
		SetApplicationID(appID).
		SetStageID(stageID).
		SetOrdinal(ordinal).
		SetName(n.Name).
		SetType(component.Type(n.Type)).
		SetConfig(nonNilStringMap(n.Config)).
		SetContinueOnFailure(n.ContinueOnFailure).
		SetRequiresApproval(n.RequiresApproval).
		SetApprovalPolicy(n.ApprovalPolicy).
		SetTargetNamespace(n.TargetNamespace)
	if n.TargetClusterID != nil && *n.TargetClusterID != uuid.Nil {
		create.SetTargetClusterID(*n.TargetClusterID)
	}
	if n.ChartCredentialID != nil && *n.ChartCredentialID != uuid.Nil {
		create.SetChartCredentialID(*n.ChartCredentialID)
	}
	if n.GitHubInstallationID != nil && *n.GitHubInstallationID != uuid.Nil {
		create.SetGithubInstallationID(*n.GitHubInstallationID)
	}
	return create.Exec(ctx)
}

// getApp confirms the application exists in the organization, returning ent's
// NotFoundError otherwise. The scoped query is the tenancy boundary for every
// workflow operation on the app.
func (s *Service) getApp(ctx context.Context, orgID, appID uuid.UUID) (*ent.Application, error) {
	return s.ent.Application.Query().
		Where(application.OrganizationID(orgID), application.ID(appID)).
		Only(ctx)
}

// GetComponent returns one component of the application, scoped to the
// organization — ent's NotFoundError when the application has no such
// component.
func (s *Service) GetComponent(ctx context.Context, orgID, appID, componentID uuid.UUID) (*ent.Component, error) {
	return s.ent.Component.Query().
		Where(
			component.OrganizationID(orgID),
			component.ApplicationID(appID),
			component.ID(componentID),
		).
		Only(ctx)
}

// validateComponentTargets checks the cluster-deploying nodes' targets against
// the database: each helm/manifest node's target cluster must exist in the
// organization, and an in-cluster target must be the application's runner
// cluster (an in-cluster API server is only reachable from a pod in that same
// cluster — the rule that used to live on the application). Terraform nodes
// carry no target (validateConfig already enforces that) and are skipped. The
// pure workflow validation has already guaranteed each helm/manifest node names a
// target cluster, so a missing one here means it isn't in the org. Failures wrap
// ErrInvalidTarget, which a handler maps to 400.
func (s *Service) validateComponentTargets(ctx context.Context, orgID uuid.UUID, app *ent.Application, nodes []ComponentInput) error {
	ids := make(map[uuid.UUID]struct{})
	for _, n := range nodes {
		if (n.Type == TypeHelm || n.Type == TypeManifest) && n.TargetClusterID != nil && *n.TargetClusterID != uuid.Nil {
			ids[*n.TargetClusterID] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	idList := make([]uuid.UUID, 0, len(ids))
	for id := range ids {
		idList = append(idList, id)
	}
	clusters, err := s.ent.Cluster.Query().
		Where(cluster.OrganizationID(orgID), cluster.IDIn(idList...)).
		All(ctx)
	if err != nil {
		return err
	}
	byID := make(map[uuid.UUID]*ent.Cluster, len(clusters))
	for _, c := range clusters {
		byID[c.ID] = c
	}
	for _, n := range nodes {
		if n.Type != TypeHelm && n.Type != TypeManifest {
			continue
		}
		if n.TargetClusterID == nil || *n.TargetClusterID == uuid.Nil {
			continue
		}
		c, ok := byID[*n.TargetClusterID]
		if !ok {
			return fmt.Errorf("%w: node %q target cluster not found in this organization", ErrInvalidTarget, n.Name)
		}
		if c.ConnectionMethod == cluster.ConnectionMethodInCluster && c.ID != app.RunnerClusterID {
			return fmt.Errorf("%w: node %q targets an in-cluster cluster that is not the application's runner; an in-cluster target requires the runner to be that same cluster (register the target via the token method with an external endpoint to use a different runner)", ErrInvalidTarget, n.Name)
		}
	}
	return nil
}

// nonNilStringMap guards against a nil map reaching the JSON column.
func nonNilStringMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// nonNilIDs guards against a nil slice reaching the JSON column.
func nonNilIDs(s []uuid.UUID) []uuid.UUID {
	if s == nil {
		return []uuid.UUID{}
	}
	return s
}

// rollback wraps tx.Rollback so a failed mutation surfaces both the original
// error and any rollback failure.
func rollback(tx *ent.Tx, err error) error {
	if rerr := tx.Rollback(); rerr != nil {
		return fmt.Errorf("%w: rollback: %v", err, rerr)
	}
	return err
}

// PluginCacheClaim returns the name of the OpenTofu provider plugin cache
// claim on a runner cluster when one is configured (tekton.PluginCacheClaim),
// or "" when the cluster has no cache. The cluster is the application's
// runner, already resolved org-scoped by the caller; the Tekton row is keyed
// by cluster id alone.
func (s *Service) PluginCacheClaim(ctx context.Context, clusterID uuid.UUID) (string, error) {
	ok, err := s.ent.TektonInstallation.Query().
		Where(tektoninstallation.ClusterID(clusterID), tektoninstallation.PluginCacheSizeNEQ("")).
		Exist(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	return tekton.PluginCacheClaim, nil
}
