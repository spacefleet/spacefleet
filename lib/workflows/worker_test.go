package workflows

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/deploy"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/tekton"
)

// TestSweepPlanArtifacts: the terminal sweep deletes one handover Secret per
// terraform plan node in the snapshot (apply and non-terraform nodes name no
// Secret of their own), and does nothing at all for a preview run or a
// snapshot without tofu plan nodes.
func TestSweepPlanArtifacts(t *testing.T) {
	t.Parallel()
	runID, planID := uuid.New(), uuid.New()
	app := &ent.Application{ID: uuid.New(), OrganizationID: uuid.New(), RunnerClusterID: uuid.New()}

	var deleted []string
	resolves := 0
	w := &WorkflowRunWorker{
		resolver: deploy.NewResolver(countingConns{&resolves}, nil, nil, nil, nil),
		deleteHandover: func(_ context.Context, _ k8s.Connection, namespace, name string) error {
			deleted = append(deleted, namespace+"/"+name)
			return nil
		},
	}

	snapshot := GraphSnapshot{Nodes: []GraphNode{
		{ID: planID, Type: TypeTerraform, Config: map[string]string{terraformConfigCommand: terraformCommandPlan}},
		{ID: uuid.New(), Type: TypeTerraform, Config: map[string]string{terraformConfigCommand: terraformCommandApply}},
		{ID: uuid.New(), Type: TypeHelm},
	}}
	a := WorkflowRunArgs{WorkflowRunID: runID, OrgID: app.OrganizationID, ApplicationID: app.ID, Action: ActionDeploy}

	w.sweepPlanArtifacts(context.Background(), a, app, snapshot)
	want := []string{tekton.JobsNamespace + "/" + tofuPlanArtifactSecret(runID, planID)}
	if !reflect.DeepEqual(deleted, want) {
		t.Errorf("deleted = %v, want %v", deleted, want)
	}

	// A preview run pre-created nothing, so nothing is swept.
	deleted = nil
	a.Action = ActionPreview
	w.sweepPlanArtifacts(context.Background(), a, app, snapshot)
	if len(deleted) != 0 {
		t.Errorf("preview sweep deleted %v, want nothing", deleted)
	}

	// A snapshot without tofu plan nodes returns before even resolving the
	// runner connection (the common helm-only run pays nothing).
	resolves = 0
	a.Action = ActionDeploy
	w.sweepPlanArtifacts(context.Background(), a, app, GraphSnapshot{Nodes: []GraphNode{{ID: uuid.New(), Type: TypeHelm}}})
	if resolves != 0 {
		t.Errorf("helm-only sweep resolved the runner connection %d times, want 0", resolves)
	}
}

// countingConns is a ConnResolver stub that counts resolutions, so a test can
// assert the sweep short-circuits before touching the cluster.
type countingConns struct{ n *int }

func (c countingConns) ConnForTekton(context.Context, uuid.UUID, uuid.UUID) (k8s.Connection, error) {
	*c.n++
	return k8s.Connection{}, nil
}
