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
