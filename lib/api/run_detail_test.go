package api

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// TestToAPIWorkflowRunDetail_RequiresApproval proves each component run's
// requires_approval is read off the run's graph snapshot (the execution unit
// with the same id), so a gated step is identifiable before it parks and after
// it was decided; a step with no snapshot node leaves the field unset.
func TestToAPIWorkflowRunDetail_RequiresApproval(t *testing.T) {
	planID, applyID, orphan := uuid.New(), uuid.New(), uuid.New()
	graph, err := json.Marshal(workflows.GraphSnapshot{Nodes: []workflows.GraphNode{
		{ID: planID, Name: "infra · plan", Type: workflows.TypeTerraform},
		{ID: applyID, Name: "infra · apply", Type: workflows.TypeTerraform, RequiresApproval: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	run := &ent.WorkflowRun{Graph: string(graph)}
	steps := []*ent.ComponentRun{
		{ComponentID: planID, Type: "terraform"},
		{ComponentID: applyID, Type: "terraform"},
		{ComponentID: orphan, Type: "helm"},
	}

	out := toAPIWorkflowRunDetail(run, steps, true)
	if got := out.ComponentRuns[0].RequiresApproval; got == nil || *got {
		t.Errorf("plan unit requires_approval = %v, want false", got)
	}
	if got := out.ComponentRuns[1].RequiresApproval; got == nil || !*got {
		t.Errorf("apply unit requires_approval = %v, want true", got)
	}
	if got := out.ComponentRuns[2].RequiresApproval; got != nil {
		t.Errorf("step without a snapshot node should leave requires_approval unset, got %v", *got)
	}

	// No snapshot at all: nothing is set, nothing panics.
	bare := toAPIWorkflowRunDetail(&ent.WorkflowRun{}, steps, true)
	for _, cr := range bare.ComponentRuns {
		if cr.RequiresApproval != nil {
			t.Errorf("unexpected requires_approval without a snapshot: %v", *cr.RequiresApproval)
		}
	}
}
