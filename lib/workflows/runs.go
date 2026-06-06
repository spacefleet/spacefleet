package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/component"
	"github.com/spacefleet/spacefleet/ent/componentrun"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
)

// ErrRunInFlight is returned by BeginRun when the application already has a run
// that is pending or running. A handler maps it to 409 — two concurrent runs
// would race the same releases on the same cluster.
var ErrRunInFlight = errors.New("workflows: a run is already in progress for this application")

// Run actions, mirroring the WorkflowRun.action enum. deploy/uninstall mutate
// the cluster; preview is a whole-workflow dry-run.
const (
	ActionDeploy    = "deploy"
	ActionUninstall = "uninstall"
	ActionPreview   = "preview"
)

// GraphSnapshot is the JSON shape stored on WorkflowRun.graph: the workflow's
// nodes (with their as-run config and targeting) so an in-flight run is immune
// to later edits and a past run stays auditable. The executor (next phase) reads
// it back; the run-detail handler exposes it (with secret config redacted).
type GraphSnapshot struct {
	Nodes []GraphNode `json:"nodes"`
}

// GraphNode is one node of a snapshot: the component as it was when the run
// began. Config is the type-specific param map (the "values" key may carry
// secrets — redacted before it reaches a viewer). depends_on are the edges.
type GraphNode struct {
	ID                   uuid.UUID         `json:"id"`
	Name                 string            `json:"name"`
	Type                 string            `json:"type"`
	Config               map[string]string `json:"config"`
	DependsOn            []uuid.UUID       `json:"depends_on"`
	ContinueOnFailure    bool              `json:"continue_on_failure"`
	TargetClusterID      *uuid.UUID        `json:"target_cluster_id,omitempty"`
	TargetNamespace      string            `json:"target_namespace,omitempty"`
	ChartCredentialID    *uuid.UUID        `json:"chart_credential_id,omitempty"`
	GitHubInstallationID *uuid.UUID        `json:"github_installation_id,omitempty"`
}

// validAction reports whether action is one of the run actions.
func validAction(action string) bool {
	switch action {
	case ActionDeploy, ActionUninstall, ActionPreview:
		return true
	default:
		return false
	}
}

// BeginRun opens a new workflow run for the application: it verifies the app
// belongs to the org, gates on no run already in flight (pending/running), then
// — in one transaction — snapshots the current component graph, creates the
// WorkflowRun (status pending), and creates one ComponentRun per node (status
// pending, with the component's id/name/type denormalized). The handler enqueues
// the job and records its id with SetRunJob.
//
// Returns ErrRunInFlight when a run is already pending/running (the handler maps
// it to 409), a ValidationError-style sentinel for an unknown action, and ent's
// NotFoundError for an application not in the org.
func (s *Service) BeginRun(ctx context.Context, orgID, appID uuid.UUID, action string) (*ent.WorkflowRun, error) {
	if !validAction(action) {
		return nil, ErrInvalidAction
	}
	if _, err := s.getApp(ctx, orgID, appID); err != nil {
		return nil, err
	}

	// In-flight gate: refuse if any run for this app is still pending or running.
	// This is the run analogue of applications.BeginRollout's settled-status guard.
	inFlight, err := s.ent.WorkflowRun.Query().
		Where(
			workflowrun.OrganizationID(orgID),
			workflowrun.ApplicationID(appID),
			workflowrun.StatusIn(workflowrun.StatusPending, workflowrun.StatusRunning),
		).
		Exist(ctx)
	if err != nil {
		return nil, err
	}
	if inFlight {
		return nil, ErrRunInFlight
	}

	comps, err := s.ent.Component.Query().
		Where(component.OrganizationID(orgID), component.ApplicationID(appID)).
		Order(ent.Asc(component.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	snapshot := snapshotComponents(comps)
	graphJSON, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}

	tx, err := s.ent.Tx(ctx)
	if err != nil {
		return nil, err
	}

	run, err := tx.WorkflowRun.Create().
		SetOrganizationID(orgID).
		SetApplicationID(appID).
		SetAction(workflowrun.Action(action)).
		SetStatus(workflowrun.StatusPending).
		SetGraph(string(graphJSON)).
		Save(ctx)
	if err != nil {
		return nil, rollback(tx, err)
	}

	for _, c := range comps {
		if err := tx.ComponentRun.Create().
			SetOrganizationID(orgID).
			SetWorkflowRunID(run.ID).
			SetComponentID(c.ID).
			SetName(c.Name).
			SetType(string(c.Type)).
			SetStatus(componentrun.StatusPending).
			Exec(ctx); err != nil {
			return nil, rollback(tx, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return run, nil
}

// snapshotComponents builds the graph snapshot from the live components, copying
// each node's as-run config and targeting. Optional FK fields are emitted only
// when set (non-zero), so a snapshot reads cleanly.
func snapshotComponents(comps []*ent.Component) GraphSnapshot {
	nodes := make([]GraphNode, 0, len(comps))
	for _, c := range comps {
		n := GraphNode{
			ID:                c.ID,
			Name:              c.Name,
			Type:              string(c.Type),
			Config:            nonNilStringMap(c.Config),
			DependsOn:         nonNilIDs(c.DependsOn),
			ContinueOnFailure: c.ContinueOnFailure,
			TargetNamespace:   c.TargetNamespace,
		}
		if c.TargetClusterID != uuid.Nil {
			id := c.TargetClusterID
			n.TargetClusterID = &id
		}
		if c.ChartCredentialID != uuid.Nil {
			id := c.ChartCredentialID
			n.ChartCredentialID = &id
		}
		if c.GithubInstallationID != uuid.Nil {
			id := c.GithubInstallationID
			n.GitHubInstallationID = &id
		}
		nodes = append(nodes, n)
	}
	return GraphSnapshot{Nodes: nodes}
}

// SetRunJob records the River job id driving a run, after the handler enqueues
// it. Org-scoped; a run not in the org updates zero rows and surfaces as a
// NotFoundError.
func (s *Service) SetRunJob(ctx context.Context, orgID, runID uuid.UUID, jobID string) error {
	affected, err := s.ent.WorkflowRun.Update().
		Where(workflowrun.OrganizationID(orgID), workflowrun.ID(runID)).
		SetJobID(jobID).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return &ent.NotFoundError{}
	}
	return nil
}

// ListRuns returns an application's workflow runs newest-first, scoped to the
// org. It first confirms the application belongs to the org so the history of an
// app the caller can't see surfaces as a NotFoundError, not an empty list.
func (s *Service) ListRuns(ctx context.Context, orgID, appID uuid.UUID) ([]*ent.WorkflowRun, error) {
	if _, err := s.getApp(ctx, orgID, appID); err != nil {
		return nil, err
	}
	return s.ent.WorkflowRun.Query().
		Where(workflowrun.OrganizationID(orgID), workflowrun.ApplicationID(appID)).
		Order(ent.Desc(workflowrun.FieldCreatedAt)).
		All(ctx)
}

// GetRun returns one workflow run and its component runs (created-order),
// strictly org-scoped and verified to belong to the application. A run not in
// the org/app surfaces as a NotFoundError.
func (s *Service) GetRun(ctx context.Context, orgID, appID, runID uuid.UUID) (*ent.WorkflowRun, []*ent.ComponentRun, error) {
	run, err := s.ent.WorkflowRun.Query().
		Where(
			workflowrun.OrganizationID(orgID),
			workflowrun.ApplicationID(appID),
			workflowrun.ID(runID),
		).
		Only(ctx)
	if err != nil {
		return nil, nil, err
	}
	steps, err := s.ent.ComponentRun.Query().
		Where(componentrun.OrganizationID(orgID), componentrun.WorkflowRunID(runID)).
		Order(ent.Asc(componentrun.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}
	return run, steps, nil
}

// GetComponentRun returns one component run within a run, verifying the run
// belongs to the app and the component run to the run, all org-scoped. Anything
// not matching surfaces as a NotFoundError.
func (s *Service) GetComponentRun(ctx context.Context, orgID, appID, runID, componentRunID uuid.UUID) (*ent.ComponentRun, error) {
	// Confirm the run belongs to the app (and org) first, so a component-run id
	// from another run can't be read by pairing it with a run the caller can see.
	if _, err := s.ent.WorkflowRun.Query().
		Where(
			workflowrun.OrganizationID(orgID),
			workflowrun.ApplicationID(appID),
			workflowrun.ID(runID),
		).
		Only(ctx); err != nil {
		return nil, err
	}
	return s.ent.ComponentRun.Query().
		Where(
			componentrun.OrganizationID(orgID),
			componentrun.WorkflowRunID(runID),
			componentrun.ID(componentRunID),
		).
		Only(ctx)
}

// MarkRun persists a run-lifecycle transition the executor drives: it sets
// started_at on the first move to running and finished_at on a terminal status.
// status is one of the WorkflowRun status strings; message is set when non-empty.
// Org-scoped; a run not in the org updates zero rows and surfaces as NotFound.
func (s *Service) MarkRun(ctx context.Context, orgID, runID uuid.UUID, status, message string) error {
	upd := s.ent.WorkflowRun.Update().
		Where(workflowrun.OrganizationID(orgID), workflowrun.ID(runID)).
		SetStatus(workflowrun.Status(status))
	if message != "" {
		upd.SetMessage(message)
	}
	now := time.Now()
	switch workflowrun.Status(status) {
	case workflowrun.StatusRunning:
		// Set started_at only if it isn't already set; ent has no conditional set,
		// so the executor calling MarkRun(running) once at the start is the contract.
		upd.SetStartedAt(now)
	case workflowrun.StatusSucceeded, workflowrun.StatusFailed, workflowrun.StatusPartial:
		upd.SetFinishedAt(now)
	}
	affected, err := upd.Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return &ent.NotFoundError{}
	}
	return nil
}

// MarkComponentRun persists a component-run transition the executor drives: it
// sets started_at on the move to running and finished_at on a terminal status,
// and records run_name (the runner TaskRun) when non-empty. Org-scoped.
func (s *Service) MarkComponentRun(ctx context.Context, orgID, componentRunID uuid.UUID, status, message, runName string) error {
	upd := s.ent.ComponentRun.Update().
		Where(componentrun.OrganizationID(orgID), componentrun.ID(componentRunID)).
		SetStatus(componentrun.Status(status))
	if message != "" {
		upd.SetMessage(message)
	}
	if runName != "" {
		upd.SetRunName(runName)
	}
	now := time.Now()
	switch componentrun.Status(status) {
	case componentrun.StatusRunning:
		upd.SetStartedAt(now)
	case componentrun.StatusSucceeded, componentrun.StatusFailed, componentrun.StatusSkipped:
		upd.SetFinishedAt(now)
	}
	affected, err := upd.Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return &ent.NotFoundError{}
	}
	return nil
}

// SetComponentRunLogs persists a component run's captured output and resolved
// revisions (chart / values), written at terminal because the runner pod is then
// garbage-collected. Each field is set only when non-empty so a partial update
// doesn't clobber an earlier write. Org-scoped.
func (s *Service) SetComponentRunLogs(ctx context.Context, orgID, componentRunID uuid.UUID, logs, chartRevision, valuesRevision string) error {
	upd := s.ent.ComponentRun.Update().
		Where(componentrun.OrganizationID(orgID), componentrun.ID(componentRunID))
	if logs != "" {
		upd.SetLogs(logs)
	}
	if chartRevision != "" {
		upd.SetChartRevision(chartRevision)
	}
	if valuesRevision != "" {
		upd.SetValuesRevision(valuesRevision)
	}
	affected, err := upd.Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return &ent.NotFoundError{}
	}
	return nil
}
