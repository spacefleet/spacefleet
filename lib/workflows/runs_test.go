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

// TestSnapshotComponents proves the graph snapshot copies each node's as-run
// config/targeting and emits optional FK ids only when set.
func TestSnapshotComponents(t *testing.T) {
	cluster := uuid.New()
	comps := []*ent.Component{
		{
			ID:                uuid.New(),
			Name:              "web",
			Type:              component.TypeHelm,
			Config:            map[string]string{"chart_source": "oci", "values": "x: 1"},
			DependsOn:         nil,
			ContinueOnFailure: true,
			TargetClusterID:   cluster,
			TargetNamespace:   "prod",
		},
		{
			ID:   uuid.New(),
			Name: "cfg",
			Type: component.TypeManifest,
			// no overrides set
		},
	}
	snap := snapshotComponents(comps, nil)
	if len(snap.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(snap.Nodes))
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
	if n0.DependsOn == nil {
		t.Fatalf("depends_on should be non-nil ([])")
	}
	n1 := snap.Nodes[1]
	if n1.TargetClusterID != nil || n1.ChartCredentialID != nil || n1.GitHubInstallationID != nil {
		t.Fatalf("node1 should have no optional ids: %+v", n1)
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
