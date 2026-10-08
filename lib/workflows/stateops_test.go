package workflows

import (
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// TestStateOpSnapshot proves a state operation's snapshot is one unit: the
// authored component (its id and config carried through) with command
// state_op, the gate forced on regardless of the component's own flag, no
// dependencies, and a name that says which operation it is.
func TestStateOpSnapshot(t *testing.T) {
	t.Parallel()
	inst := uuid.New()
	c := &ent.Component{
		ID: uuid.New(), Name: "infra", Type: "terraform",
		Config:               map[string]string{"repo_url": "r", "path": "p", terraformConfigBackend: "s3", terraformConfigPlanFlags: `["-var=env=prod"]`},
		RequiresApproval:     false,
		ContinueOnFailure:    true,
		GithubInstallationID: inst,
		Edges:                ent.ComponentEdges{Stage: &ent.WorkflowStage{ID: uuid.New(), Name: "infra"}},
	}
	snap := stateOpSnapshot(c, tofu.StateOp{Operation: tofu.StateOpRemove, Address: "aws_instance.web"})
	if len(snap.Nodes) != 1 || len(snap.Stages) != 1 {
		t.Fatalf("snapshot = %+v, want exactly one node", snap)
	}
	n := snap.Nodes[0]
	if n.ID != c.ID || n.ComponentID != c.ID || n.Type != TypeTerraform {
		t.Errorf("node identity = %+v", n)
	}
	if n.Name != "infra · state rm" {
		t.Errorf("name = %q", n.Name)
	}
	if n.Config[terraformConfigCommand] != terraformCommandStateOp || n.Config[terraformConfigPlanFlags] != `["-var=env=prod"]` {
		t.Errorf("config = %v", n.Config)
	}
	if !n.RequiresApproval || n.ContinueOnFailure || len(n.DependsOn) != 0 {
		t.Errorf("gate/deps wrong: %+v", n)
	}
	if n.StageID == nil || *n.StageID != c.Edges.Stage.ID || snap.Stages[0].ID != c.Edges.Stage.ID {
		t.Errorf("stage not carried: %v / %+v", n.StageID, snap.Stages)
	}
	if n.GitHubInstallationID == nil || *n.GitHubInstallationID != inst {
		t.Errorf("installation not carried: %+v", n.GitHubInstallationID)
	}
	// The authored config map is not shared with the snapshot node.
	if _, ok := c.Config[terraformConfigCommand]; ok {
		t.Error("stateOpSnapshot mutated the component's config")
	}
	if !ownsTofuHandover(n) || tofuHandoverPlanID(n, nil) != n.ID {
		t.Error("a state-op unit must own its handover Secret under its own id")
	}
	if !recordsTofuState(ActionStateOp, n) || recordsTofuState(ActionDeploy, n) {
		t.Error("a state-op unit records state only on a state_op run")
	}
	applyUnit := GraphNode{Type: TypeTerraform, Config: map[string]string{terraformConfigCommand: terraformCommandApply}}
	if !recordsTofuState(ActionDeploy, applyUnit) || !recordsTofuState(ActionUninstall, applyUnit) || recordsTofuState(ActionPreview, applyUnit) {
		t.Error("an apply unit records state on deploy and on destroy, never on a preview")
	}
}

// TestStateOpOf proves the run-row decode: nothing for other actions, the
// operation for a state_op run, an error for a state_op run with bad args.
func TestStateOpOf(t *testing.T) {
	t.Parallel()
	if _, ok, err := StateOpOf(&ent.WorkflowRun{Action: "deploy", Args: `{"operation":"rm"}`}); ok || err != nil {
		t.Errorf("deploy: ok=%v err=%v, want neither", ok, err)
	}
	op, ok, err := StateOpOf(&ent.WorkflowRun{Action: ActionStateOp, Args: `{"operation":"mv","address":"a","new_address":"b"}`})
	if err != nil || !ok || op.Operation != tofu.StateOpMove || op.NewAddress != "b" {
		t.Errorf("state_op: op=%+v ok=%v err=%v", op, ok, err)
	}
	for _, args := range []string{"", "{"} {
		if _, _, err := StateOpOf(&ent.WorkflowRun{Action: ActionStateOp, Args: args}); err == nil {
			t.Errorf("args %q: want an error", args)
		}
	}
}
