package workflows

import (
	"testing"

	"github.com/google/uuid"
)

// findNode returns the first node with the given id, or fails.
func findNode(t *testing.T, nodes []GraphNode, id uuid.UUID) GraphNode {
	t.Helper()
	for _, n := range nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("node %s not found in expansion", id)
	return GraphNode{}
}

// TestExpandExecutionNodes_TerraformSplit proves a single terraform component
// expands into a plan unit (the authored id) and an apply unit (the derived id),
// with the command synthesized per unit, the approval gate + continue-on-failure
// carried onto the apply only, and both units resolving under the authored
// component id.
func TestExpandExecutionNodes_TerraformSplit(t *testing.T) {
	t.Parallel()

	tfID := uuid.New()
	upstream := uuid.New()
	tf := GraphNode{
		ID:                tfID,
		ComponentID:       tfID,
		Name:              "infra",
		Type:              TypeTerraform,
		Config:            map[string]string{terraformConfigBackend: "s3", helmConfigChartSource: ""},
		DependsOn:         []uuid.UUID{upstream},
		RequiresApproval:  true,
		ContinueOnFailure: true,
	}
	helmUp := GraphNode{ID: upstream, ComponentID: upstream, Name: "db", Type: TypeHelm}

	out := expandExecutionNodes([]GraphNode{helmUp, tf})

	// helm (passthrough) + plan + apply.
	if len(out) != 3 {
		t.Fatalf("expected 3 execution nodes, got %d", len(out))
	}

	plan := findNode(t, out, tfID)
	applyID := deriveApplyID(tfID)
	apply := findNode(t, out, applyID)

	// Plan: keeps the authored id, runs plan, never gated, never continue-on-fail,
	// keeps the component's upstream deps.
	if plan.Config[terraformConfigCommand] != terraformCommandPlan {
		t.Errorf("plan command = %q, want plan", plan.Config[terraformConfigCommand])
	}
	if plan.RequiresApproval || plan.ContinueOnFailure {
		t.Errorf("plan must not be gated or continue-on-failure: %+v", plan)
	}
	if len(plan.DependsOn) != 1 || plan.DependsOn[0] != upstream {
		t.Errorf("plan deps = %v, want [%s]", plan.DependsOn, upstream)
	}
	if plan.ComponentID != tfID {
		t.Errorf("plan ComponentID = %s, want authored %s", plan.ComponentID, tfID)
	}

	// Apply: derived id, runs apply, carries the gate + continue-on-failure,
	// depends on exactly its plan, resolves under the authored component id.
	if apply.Config[terraformConfigCommand] != terraformCommandApply {
		t.Errorf("apply command = %q, want apply", apply.Config[terraformConfigCommand])
	}
	if !apply.RequiresApproval || !apply.ContinueOnFailure {
		t.Errorf("apply must carry the gate + continue-on-failure: %+v", apply)
	}
	if len(apply.DependsOn) != 1 || apply.DependsOn[0] != tfID {
		t.Errorf("apply deps = %v, want [%s] (the plan unit)", apply.DependsOn, tfID)
	}
	if apply.ComponentID != tfID {
		t.Errorf("apply ComponentID = %s, want authored %s", apply.ComponentID, tfID)
	}

	// The plan and apply config maps are independent copies (no shared command).
	if &plan.Config == &apply.Config {
		t.Errorf("plan and apply must not share a config map")
	}
	// The authored node's config is not mutated (no command leaked back in).
	if _, ok := tf.Config[terraformConfigCommand]; ok {
		t.Errorf("authored config was mutated with a command key: %v", tf.Config)
	}
}

// TestExpandExecutionNodes_RewiresDependents proves a node depending on a
// terraform component is rewired onto its apply unit — the deployment completes
// when apply finishes, not when plan does — including a terraform→terraform edge.
func TestExpandExecutionNodes_RewiresDependents(t *testing.T) {
	t.Parallel()

	tfA := uuid.New()
	tfB := uuid.New()
	consumer := uuid.New()

	nodes := []GraphNode{
		{ID: tfA, ComponentID: tfA, Name: "a", Type: TypeTerraform, Config: map[string]string{terraformConfigBackend: "s3"}},
		// B depends on A (terraform → terraform).
		{ID: tfB, ComponentID: tfB, Name: "b", Type: TypeTerraform, Config: map[string]string{terraformConfigBackend: "s3"}, DependsOn: []uuid.UUID{tfA}},
		// A helm consumer depends on B.
		{ID: consumer, ComponentID: consumer, Name: "c", Type: TypeHelm, DependsOn: []uuid.UUID{tfB}},
	}

	out := expandExecutionNodes(nodes)
	if len(out) != 5 { // 2 tofu × 2 + 1 helm
		t.Fatalf("expected 5 execution nodes, got %d", len(out))
	}

	applyA := deriveApplyID(tfA)
	applyB := deriveApplyID(tfB)

	// B's plan unit should depend on A's apply unit (not A's plan).
	planB := findNode(t, out, tfB)
	if len(planB.DependsOn) != 1 || planB.DependsOn[0] != applyA {
		t.Errorf("B plan deps = %v, want [%s] (A's apply)", planB.DependsOn, applyA)
	}

	// The helm consumer should depend on B's apply unit.
	c := findNode(t, out, consumer)
	if len(c.DependsOn) != 1 || c.DependsOn[0] != applyB {
		t.Errorf("consumer deps = %v, want [%s] (B's apply)", c.DependsOn, applyB)
	}
}

// TestExpandExecutionNodes_NonTerraformUnchanged proves helm/manifest nodes pass
// through with id/ComponentID intact and only terraform deps remapped.
func TestExpandExecutionNodes_NonTerraformUnchanged(t *testing.T) {
	t.Parallel()

	h := uuid.New()
	m := uuid.New()
	nodes := []GraphNode{
		{ID: h, ComponentID: h, Name: "h", Type: TypeHelm},
		{ID: m, ComponentID: m, Name: "m", Type: TypeManifest, DependsOn: []uuid.UUID{h}},
	}
	out := expandExecutionNodes(nodes)
	if len(out) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(out))
	}
	mn := findNode(t, out, m)
	if len(mn.DependsOn) != 1 || mn.DependsOn[0] != h {
		t.Errorf("manifest deps = %v, want [%s] unchanged", mn.DependsOn, h)
	}
}

// TestDeriveApplyID_Deterministic proves the apply id derivation is stable (a
// River retry re-derives the same id from the immutable snapshot) and distinct
// from the plan id.
func TestDeriveApplyID_Deterministic(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	a, b := deriveApplyID(id), deriveApplyID(id)
	if a != b {
		t.Errorf("deriveApplyID is not deterministic: %s != %s", a, b)
	}
	if a == id {
		t.Errorf("apply id must differ from the plan (authored) id")
	}
	if a == deriveApplyID(uuid.New()) {
		t.Errorf("distinct components must derive distinct apply ids")
	}
}
