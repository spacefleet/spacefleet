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
//
// Group containers are a builder concept the scheduler never sees: each node's
// depends_on is already the *expanded*, pure component-level edge set (groups
// desugared away), so the executor consumes it directly. Groups carries the
// authored boxes purely so a future run view can render them; it is not used by
// the scheduler.
type GraphSnapshot struct {
	Nodes  []GraphNode  `json:"nodes"`
	Groups []GraphGroup `json:"groups,omitempty"`
}

// GraphNode is one node of a snapshot: the component as it was when the run
// began. Config is the type-specific param map (the "values" key may carry
// secrets — redacted before it reaches a viewer). depends_on are the *expanded*
// component-level edges (group references desugared into member ids). group_id
// records the authored group membership for the run view only.
type GraphNode struct {
	ID                   uuid.UUID         `json:"id"`
	Name                 string            `json:"name"`
	Type                 string            `json:"type"`
	Config               map[string]string `json:"config"`
	DependsOn            []uuid.UUID       `json:"depends_on"`
	ContinueOnFailure    bool              `json:"continue_on_failure"`
	RequiresApproval     bool              `json:"requires_approval"`
	TargetClusterID      *uuid.UUID        `json:"target_cluster_id,omitempty"`
	TargetNamespace      string            `json:"target_namespace,omitempty"`
	ChartCredentialID    *uuid.UUID        `json:"chart_credential_id,omitempty"`
	GitHubInstallationID *uuid.UUID        `json:"github_installation_id,omitempty"`
	GroupID              *uuid.UUID        `json:"group_id,omitempty"`
}

// GraphGroup is one authored group container in a snapshot, retained so a future
// run view can draw the boxes. The scheduler ignores it — node edges are already
// expanded. Members are the component ids whose group_id is this group.
type GraphGroup struct {
	ID       uuid.UUID          `json:"id"`
	Name     string             `json:"name"`
	Members  []uuid.UUID        `json:"members"`
	Position map[string]float64 `json:"position,omitempty"`
	Size     map[string]float64 `json:"size,omitempty"`
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
			workflowrun.StatusIn(workflowrun.StatusPending, workflowrun.StatusRunning, workflowrun.StatusAwaitingApproval),
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
	groups, err := s.listGroups(ctx, orgID, appID)
	if err != nil {
		return nil, err
	}

	snapshot := snapshotComponents(comps, groups)
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

// snapshotComponents builds the graph snapshot from the live components and group
// containers, copying each node's as-run config and targeting. Group references
// are desugared away: each node's depends_on is the *expanded* component-level
// edge set (a dependency on a group becomes a dependency on every member), so the
// scheduler — which never sees a group — consumes it directly. The authored
// groups are retained on the snapshot (with member ids) only so a future run view
// can render the boxes. Optional FK fields are emitted only when set (non-zero),
// so a snapshot reads cleanly.
func snapshotComponents(comps []*ent.Component, groups []*ent.ComponentGroup) GraphSnapshot {
	// Reuse the pure expansion: build the inputs from the live rows.
	compInputs := make([]ComponentInput, 0, len(comps))
	for _, c := range comps {
		in := ComponentInput{ID: c.ID, DependsOn: c.DependsOn}
		if c.GroupID != uuid.Nil {
			gid := c.GroupID
			in.GroupID = &gid
		}
		compInputs = append(compInputs, in)
	}
	groupInputs := make([]GroupInput, 0, len(groups))
	for _, g := range groups {
		groupInputs = append(groupInputs, GroupInput{ID: g.ID, DependsOn: g.DependsOn})
	}
	expanded := expandDependencies(compInputs, groupInputs)

	nodes := make([]GraphNode, 0, len(comps))
	for _, c := range comps {
		n := GraphNode{
			ID:                c.ID,
			Name:              c.Name,
			Type:              string(c.Type),
			Config:            nonNilStringMap(c.Config),
			DependsOn:         nonNilIDs(expanded[c.ID]),
			ContinueOnFailure: c.ContinueOnFailure,
			RequiresApproval:  c.RequiresApproval,
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
		if c.GroupID != uuid.Nil {
			id := c.GroupID
			n.GroupID = &id
		}
		nodes = append(nodes, n)
	}

	// members[g] = component ids whose group_id == g, for the run-view boxes.
	members := make(map[uuid.UUID][]uuid.UUID, len(groups))
	for _, c := range comps {
		if c.GroupID != uuid.Nil {
			members[c.GroupID] = append(members[c.GroupID], c.ID)
		}
	}
	var graphGroups []GraphGroup
	for _, g := range groups {
		gg := GraphGroup{
			ID:      g.ID,
			Name:    g.Name,
			Members: nonNilIDs(members[g.ID]),
		}
		if len(g.Position) > 0 {
			gg.Position = g.Position
		}
		if len(g.Size) > 0 {
			gg.Size = g.Size
		}
		graphGroups = append(graphGroups, gg)
	}
	return GraphSnapshot{Nodes: nodes, Groups: graphGroups}
}

// Approval decisions for ApproveComponentRun.
const (
	DecisionApprove = "approve"
	DecisionReject  = "reject"
)

// ErrInvalidDecision is returned by ApproveComponentRun for a decision that isn't
// approve or reject. A handler maps it to 400.
var ErrInvalidDecision = errors.New("workflows: invalid approval decision")

// ErrNotAwaitingApproval is returned by ApproveComponentRun when the run or its
// component run is not parked awaiting approval — there is nothing to decide. A
// handler maps it to 409.
var ErrNotAwaitingApproval = errors.New("workflows: run or component is not awaiting approval")

// ApproveComponentRun records a manual-approval decision on a parked gate and
// returns the run so the handler can re-enqueue the resume job. It verifies — all
// org-scoped, and confirming the run belongs to the app and the component run to
// the run — that the run is awaiting_approval and the target component run is
// awaiting_approval, then, in one transaction:
//
//   - approve: stamps approved_by/approved_at and moves the component run back to
//     pending so the resumed worker re-evaluates it as runnable (the gate cleared).
//   - reject: settles the component run failed ("approval rejected"); its dependents
//     skip on resume and the run settles failed/partial.
//
// In both cases the run is left awaiting_approval here — the handler enqueues a
// resume job, whose worker flips the run back to running (and on to terminal). The
// status guards are re-asserted as predicates inside the tx so a concurrent decision
// or a cancel that settled the run first makes this a no-op (ErrNotAwaitingApproval).
func (s *Service) ApproveComponentRun(ctx context.Context, orgID, appID, runID, crID uuid.UUID, approver, decision string) (*ent.WorkflowRun, error) {
	if decision != DecisionApprove && decision != DecisionReject {
		return nil, ErrInvalidDecision
	}

	run, err := s.ent.WorkflowRun.Query().
		Where(
			workflowrun.OrganizationID(orgID),
			workflowrun.ApplicationID(appID),
			workflowrun.ID(runID),
		).
		Only(ctx)
	if err != nil {
		return nil, err
	}
	if run.Status != workflowrun.StatusAwaitingApproval {
		return nil, ErrNotAwaitingApproval
	}

	// Confirm the component run belongs to this run (and org) and is the parked gate.
	cr, err := s.ent.ComponentRun.Query().
		Where(
			componentrun.OrganizationID(orgID),
			componentrun.WorkflowRunID(runID),
			componentrun.ID(crID),
		).
		Only(ctx)
	if err != nil {
		return nil, err
	}
	if cr.Status != componentrun.StatusAwaitingApproval {
		return nil, ErrNotAwaitingApproval
	}

	tx, err := s.ent.Tx(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	upd := tx.ComponentRun.Update().
		Where(
			componentrun.OrganizationID(orgID),
			componentrun.WorkflowRunID(runID),
			componentrun.ID(crID),
			componentrun.StatusEQ(componentrun.StatusAwaitingApproval),
		)
	switch decision {
	case DecisionApprove:
		upd.SetApprovedBy(approver).
			SetApprovedAt(now).
			SetStatus(componentrun.StatusPending).
			SetMessage("approved by " + approver)
	case DecisionReject:
		upd.SetApprovedBy(approver).
			SetApprovedAt(now).
			SetStatus(componentrun.StatusFailed).
			SetMessage("approval rejected").
			SetFinishedAt(now)
	}
	affected, err := upd.Save(ctx)
	if err != nil {
		return nil, rollback(tx, err)
	}
	if affected == 0 {
		return nil, rollback(tx, ErrNotAwaitingApproval)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// Re-read the run so the handler returns the current row (still awaiting_approval
	// until the resume job it enqueues flips it back to running).
	return s.ent.WorkflowRun.Query().
		Where(workflowrun.OrganizationID(orgID), workflowrun.ID(runID)).
		Only(ctx)
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
//
// The update is guarded to only transition a run that is still in flight
// (pending/running/awaiting_approval): once a run has settled (succeeded/failed/
// partial) it is frozen. This makes a terminal status durable — the worker's final
// MarkRun(final) at the end of a run can no longer resurrect a run that CancelRun or
// the reaper already failed (it becomes a no-op), closing the cancel-vs-executor
// race. The legitimate transitions the executor drives (pending→running,
// running→awaiting_approval when a gate parks the DAG, awaiting_approval→running on
// resume, and →terminal) all start from an in-flight state, so they are unaffected.
func (s *Service) MarkRun(ctx context.Context, orgID, runID uuid.UUID, status, message string) error {
	upd := s.ent.WorkflowRun.Update().
		Where(
			workflowrun.OrganizationID(orgID),
			workflowrun.ID(runID),
			workflowrun.StatusIn(workflowrun.StatusPending, workflowrun.StatusRunning, workflowrun.StatusAwaitingApproval),
		).
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

// SettleStuckComponentRuns sweeps every still-non-terminal component run of a
// run (pending / running / awaiting_approval) to a terminal "skipped" status.
// It exists for the worker's terminal path: when the scheduler returns a failed
// (or partial) run because a hard failure on one branch outranks a gate that
// parked another branch, the parked node was emitted awaiting_approval and
// persisted, but the scheduler deliberately never settles a parked node. The
// run's own MarkRun only touches the workflow_runs row, so without this sweep
// that component run would sit at awaiting_approval forever under a terminal
// run — an unrecoverable inconsistent state (approve/reject and cancel both
// require an in-flight run). Mirrors CancelRun's settle-steps sweep; terminal
// steps are left untouched, so it never clobbers a real succeeded/failed/skipped
// result. Org-scoped. Returns the number of steps settled.
func (s *Service) SettleStuckComponentRuns(ctx context.Context, orgID, runID uuid.UUID, message string) (int, error) {
	if message == "" {
		message = "skipped (run did not complete)"
	}
	return s.ent.ComponentRun.Update().
		Where(
			componentrun.OrganizationID(orgID),
			componentrun.WorkflowRunID(runID),
			componentrun.StatusIn(componentrun.StatusPending, componentrun.StatusRunning, componentrun.StatusAwaitingApproval),
		).
		SetStatus(componentrun.StatusSkipped).
		SetMessage(message).
		SetFinishedAt(time.Now()).
		Save(ctx)
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
