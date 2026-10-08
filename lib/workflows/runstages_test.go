package workflows

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/componentrun"
)

// step builds a component run for an execution node.
func step(nodeID uuid.UUID, status componentrun.Status) *ent.ComponentRun {
	return &ent.ComponentRun{ID: uuid.New(), ComponentID: nodeID, Status: status}
}

func mustGraph(t *testing.T, snap GraphSnapshot) string {
	t.Helper()
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRunStages_Recorded proves a run that recorded its stages is summarized
// by them: an OpenTofu component's plan and apply steps fold into one
// component (named without the step suffix, listing both step ids, statuses
// combined), components keep their stage, and each stage's status combines
// its components'.
func TestRunStages_Recorded(t *testing.T) {
	infra := &ent.Component{ID: uuid.New(), Name: "infra", Type: "terraform"}
	web := &ent.Component{ID: uuid.New(), Name: "web", Type: "helm"}
	api := &ent.Component{ID: uuid.New(), Name: "api", Type: "helm"}
	s1 := &ent.WorkflowStage{ID: uuid.New(), Name: "Infrastructure", Edges: ent.WorkflowStageEdges{Components: []*ent.Component{infra}}}
	s2 := &ent.WorkflowStage{ID: uuid.New(), Name: "Apps", Edges: ent.WorkflowStageEdges{Components: []*ent.Component{web, api}}}
	graph := mustGraph(t, snapshotComponents([]*ent.WorkflowStage{s1, s2}, ActionDeploy))

	plan := step(infra.ID, componentrun.StatusSucceeded)
	apply := step(deriveApplyID(infra.ID), componentrun.StatusAwaitingApproval)
	steps := []*ent.ComponentRun{plan, apply, step(web.ID, componentrun.StatusPending), step(api.ID, componentrun.StatusPending)}

	got := RunStages(graph, steps)
	if len(got) != 2 || got[0].Name != "Infrastructure" || got[1].Name != "Apps" {
		t.Fatalf("stages = %+v", got)
	}
	tf := got[0].Components
	if len(tf) != 1 || tf[0].Name != "infra" || tf[0].ComponentID != infra.ID || tf[0].Type != "terraform" {
		t.Fatalf("infra component = %+v", tf)
	}
	if len(tf[0].ComponentRunIDs) != 2 || tf[0].ComponentRunIDs[0] != plan.ID || tf[0].ComponentRunIDs[1] != apply.ID {
		t.Errorf("infra steps = %v, want plan then apply", tf[0].ComponentRunIDs)
	}
	if tf[0].Status != stepAwaitingApproval || got[0].Status != stepAwaitingApproval {
		t.Errorf("infra = %s, stage = %s; want awaiting_approval", tf[0].Status, got[0].Status)
	}
	if len(got[1].Components) != 2 || got[1].Components[0].Name != "web" || got[1].Components[1].Name != "api" {
		t.Errorf("apps components = %+v", got[1].Components)
	}
	if got[1].Status != stepPending {
		t.Errorf("apps stage = %s, want pending", got[1].Status)
	}
}

// TestRunStages_Legacy proves a run from before stages existed — no recorded
// stages, the old "groups" key, explicit depends_on — is placed by its longest
// chain of dependencies, with the plan → apply edge inside one component not
// counted, and the stages named in order.
func TestRunStages_Legacy(t *testing.T) {
	db, tfPlan := uuid.New(), uuid.New()
	tfApply := deriveApplyID(tfPlan)
	web, side := uuid.New(), uuid.New()
	graph := `{"nodes":[` +
		`{"id":"` + db.String() + `","component_id":"` + db.String() + `","name":"db","type":"helm","depends_on":[]},` +
		`{"id":"` + tfPlan.String() + `","component_id":"` + tfPlan.String() + `","name":"infra · plan","type":"terraform","depends_on":["` + db.String() + `"]},` +
		`{"id":"` + tfApply.String() + `","component_id":"` + tfPlan.String() + `","name":"infra · apply","type":"terraform","depends_on":["` + tfPlan.String() + `"]},` +
		`{"id":"` + web.String() + `","name":"web","type":"helm","depends_on":["` + tfApply.String() + `","` + uuid.NewString() + `"]},` +
		`{"id":"` + side.String() + `","name":"side","type":"manifest","depends_on":[]}` +
		`],"groups":[{"id":"` + uuid.NewString() + `","name":"g","members":[]}]}`

	got := RunStages(graph, []*ent.ComponentRun{
		step(db, componentrun.StatusSucceeded),
		step(tfPlan, componentrun.StatusSucceeded),
		step(tfApply, componentrun.StatusFailed),
		step(web, componentrun.StatusSkipped),
		step(side, componentrun.StatusSucceeded),
	})
	if len(got) != 3 {
		t.Fatalf("stages = %+v, want 3", got)
	}
	names := func(st RunStage) []string {
		var out []string
		for _, c := range st.Components {
			out = append(out, c.Name)
		}
		return out
	}
	if got[0].Name != "Stage 1" || len(got[0].Components) != 2 || names(got[0])[0] != "db" || names(got[0])[1] != "side" {
		t.Errorf("stage 1 = %s %v", got[0].Name, names(got[0]))
	}
	if got[1].Name != "Stage 2" || len(got[1].Components) != 1 || got[1].Components[0].Name != "infra" || got[1].Status != stepFailed {
		t.Errorf("stage 2 = %s %v %s", got[1].Name, names(got[1]), got[1].Status)
	}
	// A node with no component_id is its own component.
	if got[2].Name != "Stage 3" || got[2].Components[0].ComponentID != web || got[2].Status != stepSkipped {
		t.Errorf("stage 3 = %+v", got[2])
	}
	if got[0].Status != stepSucceeded {
		t.Errorf("stage 1 status = %s", got[0].Status)
	}
}

// TestRunStages_Fallbacks covers a run whose snapshot is unreadable (each step
// listed on its own in one stage), a step with no component run yet
// (pending), and recorded stages that don't cover every component (derived
// instead, rather than dropping the stray component).
func TestRunStages_Fallbacks(t *testing.T) {
	a := step(uuid.New(), componentrun.StatusSucceeded)
	a.Name, a.Type = "web", "helm"
	got := RunStages("{not json", []*ent.ComponentRun{a})
	if len(got) != 1 || got[0].Name != "Stage 1" || len(got[0].Components) != 1 || got[0].Components[0].ComponentRunIDs[0] != a.ID {
		t.Errorf("unreadable snapshot = %+v", got)
	}
	if got := RunStages("", nil); len(got) != 0 {
		t.Errorf("no snapshot, no steps = %+v", got)
	}

	stageID := uuid.New()
	n1, n2 := uuid.New(), uuid.New()
	snap := GraphSnapshot{
		Stages: []GraphStage{{ID: stageID, Name: "deploy"}},
		Nodes: []GraphNode{
			{ID: n1, ComponentID: n1, Name: "one", Type: "helm", StageID: &stageID, DependsOn: []uuid.UUID{}},
			{ID: n2, ComponentID: n2, Name: "two", Type: "helm", DependsOn: []uuid.UUID{n1}},
		},
	}
	got = RunStages(mustGraph(t, snap), nil)
	if len(got) != 2 || got[0].Name != "Stage 1" || got[1].Components[0].Name != "two" {
		t.Fatalf("partial stage record = %+v, want derived stages", got)
	}
	if got[0].Components[0].Status != stepPending || len(got[0].Components[0].ComponentRunIDs) != 0 {
		t.Errorf("a step with no component run = %+v, want pending with no ids", got[0].Components[0])
	}
}

func TestCombineStatus(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, stepPending},
		{[]string{stepPending, stepPending}, stepPending},
		{[]string{stepSucceeded, stepSucceeded}, stepSucceeded},
		{[]string{stepSucceeded, stepPending}, stepRunning},
		{[]string{stepSucceeded, stepRunning}, stepRunning},
		{[]string{stepRunning, stepAwaitingApproval}, stepAwaitingApproval},
		{[]string{stepAwaitingApproval, stepFailed}, stepFailed},
		{[]string{stepSucceeded, stepSkipped}, stepSkipped},
		{[]string{stepSkipped}, stepSkipped},
	}
	for _, c := range cases {
		if got := combineStatus(c.in); got != c.want {
			t.Errorf("combineStatus(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}
