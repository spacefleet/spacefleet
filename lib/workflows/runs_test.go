package workflows

import (
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/component"
)

// TestValidAction covers the run-action allowlist BeginRun gates on.
func TestValidAction(t *testing.T) {
	for _, a := range []string{ActionDeploy, ActionUninstall, ActionPreview} {
		if !validAction(a) {
			t.Errorf("%q should be valid", a)
		}
	}
	for _, a := range []string{"", "rollout", "Deploy", "upgrade"} {
		if validAction(a) {
			t.Errorf("%q should be invalid", a)
		}
	}
}

// TestSnapshotComponents proves the graph snapshot copies each component's
// as-run config/targeting, emits optional FK ids only when set, records the
// stages that hold a component, and desugars stage order into depends_on:
// every component waits on every component of the previous non-empty stage.
func TestSnapshotComponents(t *testing.T) {
	cluster := uuid.New()
	web := &ent.Component{
		ID:                uuid.New(),
		Name:              "web",
		Type:              component.TypeHelm,
		Config:            map[string]string{"chart_source": "oci", "values": "x: 1"},
		ContinueOnFailure: true,
		TargetClusterID:   cluster,
		TargetNamespace:   "prod",
	}
	cfg := &ent.Component{
		ID:   uuid.New(),
		Name: "cfg",
		Type: component.TypeManifest,
		// no overrides set
	}
	worker := &ent.Component{ID: uuid.New(), Name: "worker", Type: component.TypeManifest}
	stage := func(name string, comps ...*ent.Component) *ent.WorkflowStage {
		return &ent.WorkflowStage{ID: uuid.New(), Name: name, Edges: ent.WorkflowStageEdges{Components: comps}}
	}
	first, empty, second := stage("build", web, cfg), stage("empty"), stage("deploy", worker)

	snap := snapshotComponents([]*ent.WorkflowStage{first, empty, second}, ActionDeploy)
	if len(snap.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3", len(snap.Nodes))
	}
	n0 := snap.Nodes[0]
	if n0.Name != "web" || n0.Type != "helm" || !n0.ContinueOnFailure {
		t.Fatalf("node0 not mapped: %+v", n0)
	}
	if n0.Config["values"] != "x: 1" {
		t.Fatalf("node0 config not copied: %v", n0.Config)
	}
	if n0.TargetClusterID == nil || *n0.TargetClusterID != cluster {
		t.Fatalf("node0 target cluster not set: %v", n0.TargetClusterID)
	}
	if n0.DependsOn == nil || len(n0.DependsOn) != 0 {
		t.Fatalf("a first-stage component depends on nothing, as [] (got %v)", n0.DependsOn)
	}
	n1 := snap.Nodes[1]
	if n1.TargetClusterID != nil || n1.ChartCredentialID != nil || n1.GitHubInstallationID != nil {
		t.Fatalf("node1 should have no optional ids: %+v", n1)
	}
	if len(n1.DependsOn) != 0 {
		t.Fatalf("components of one stage run in parallel; got deps %v", n1.DependsOn)
	}
	n2 := snap.Nodes[2]
	if len(n2.DependsOn) != 2 || n2.DependsOn[0] != web.ID || n2.DependsOn[1] != cfg.ID {
		t.Fatalf("worker should wait on the whole previous non-empty stage, got %v", n2.DependsOn)
	}
	for i, n := range snap.Nodes {
		want := first.ID
		if i == 2 {
			want = second.ID
		}
		if n.StageID == nil || *n.StageID != want {
			t.Errorf("node %d stage = %v, want %s", i, n.StageID, want)
		}
	}
	// The empty stage has nothing to show, so it isn't recorded.
	if len(snap.Stages) != 2 || snap.Stages[0] != (GraphStage{ID: first.ID, Name: "build"}) || snap.Stages[1] != (GraphStage{ID: second.ID, Name: "deploy"}) {
		t.Fatalf("stages = %+v, want build then deploy", snap.Stages)
	}
}

// TestSnapshotComponents_TerraformStage proves an OpenTofu component's plan
// and apply units both keep its stage, and that a later stage waits on the
// apply (the component only completes once it has applied).
func TestSnapshotComponents_TerraformStage(t *testing.T) {
	infra := &ent.Component{ID: uuid.New(), Name: "infra", Type: component.TypeTerraform}
	web := &ent.Component{ID: uuid.New(), Name: "web", Type: component.TypeHelm}
	s1 := &ent.WorkflowStage{ID: uuid.New(), Name: "infra", Edges: ent.WorkflowStageEdges{Components: []*ent.Component{infra}}}
	s2 := &ent.WorkflowStage{ID: uuid.New(), Name: "apps", Edges: ent.WorkflowStageEdges{Components: []*ent.Component{web}}}

	snap := snapshotComponents([]*ent.WorkflowStage{s1, s2}, ActionDeploy)
	if len(snap.Nodes) != 3 {
		t.Fatalf("nodes = %d, want plan + apply + web", len(snap.Nodes))
	}
	plan, apply, w := snap.Nodes[0], snap.Nodes[1], snap.Nodes[2]
	if *plan.StageID != s1.ID || *apply.StageID != s1.ID || *w.StageID != s2.ID {
		t.Errorf("stages = %v / %v / %v", *plan.StageID, *apply.StageID, *w.StageID)
	}
	if len(w.DependsOn) != 1 || w.DependsOn[0] != deriveApplyID(infra.ID) {
		t.Errorf("web deps = %v, want the infra apply unit", w.DependsOn)
	}
}

// TestParseOutputKeys proves the keys-only projection of a stored tofu
// `output -json` blob: names sorted, sensitivity preserved, the type descriptor
// surfaced as a hint (a bare null dropped), and never a value. Empty or garbled
// input yields nil so a component simply has no known keys.
func TestParseOutputKeys(t *testing.T) {
	for _, raw := range []string{"", "{not json", "{}", "[]"} {
		if got := parseOutputKeys(raw); got != nil {
			t.Errorf("parseOutputKeys(%q) = %v, want nil", raw, got)
		}
	}

	raw := `{` +
		`"vpc_id":{"value":"vpc-123","type":"string","sensitive":false},` +
		`"db_password":{"value":"hunter2","type":"string","sensitive":true},` +
		`"ports":{"value":[80,443],"type":["list","number"],"sensitive":false},` +
		`"untyped":{"value":"x","type":null,"sensitive":false}` +
		`}`
	got := parseOutputKeys(raw)
	wantOrder := []string{"db_password", "ports", "untyped", "vpc_id"}
	if len(got) != len(wantOrder) {
		t.Fatalf("keys = %v, want %v (sorted)", got, wantOrder)
	}
	byKey := map[string]OutputKey{}
	for i, k := range got {
		if k.Key != wantOrder[i] {
			t.Errorf("keys[%d].Key = %q, want %q (sorted by name)", i, k.Key, wantOrder[i])
		}
		byKey[k.Key] = k
	}
	if !byKey["db_password"].Sensitive {
		t.Error("db_password should be sensitive")
	}
	if byKey["vpc_id"].Sensitive {
		t.Error("vpc_id should not be sensitive")
	}
	// The type descriptor is itself JSON, surfaced verbatim as a display hint.
	if byKey["vpc_id"].Type != `"string"` {
		t.Errorf("vpc_id type = %q, want %q", byKey["vpc_id"].Type, `"string"`)
	}
	if byKey["ports"].Type != `["list","number"]` {
		t.Errorf("ports type = %q, want the compact JSON array", byKey["ports"].Type)
	}
	// A null type descriptor is dropped (no hint), not surfaced as "null".
	if byKey["untyped"].Type != "" {
		t.Errorf("untyped type = %q, want empty (null dropped)", byKey["untyped"].Type)
	}
}
