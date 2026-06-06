package workflows

import "github.com/google/uuid"

// WorkflowRunArgs is the River job enqueued by the API when a workflow run is
// started. It carries only ids — never credentials; the worker (next phase)
// re-opens the run, its graph snapshot, and any sealed credentials through the
// org-scoped service. Defined here so the API can enqueue it before the worker
// exists; the WorkflowRunWorker that satisfies it lands in Phase 3.
type WorkflowRunArgs struct {
	WorkflowRunID uuid.UUID `json:"workflow_run_id"`
	OrgID         uuid.UUID `json:"org_id"`
	ApplicationID uuid.UUID `json:"application_id"`
	Action        string    `json:"action"`
}

// Kind is the stable River job identifier.
func (WorkflowRunArgs) Kind() string { return "workflow_run" }
