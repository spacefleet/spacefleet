package workflows

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
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

// TestNormalizeTofuOutputs: valid `tofu output -json` round-trips (non-string
// values intact), empties yield "" (no column write), and non-tofu shapes are
// an error — never silently persisted.
func TestNormalizeTofuOutputs(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"namespace": {"sensitive": false, "type": "string", "value": "customer-a"},
		"db_password": {"sensitive": true, "type": "string", "value": "hunter2"},
		"subnet_ids": {"sensitive": false, "type": ["list", "string"], "value": ["a", "b"]}
	}`)
	got, err := normalizeTofuOutputs(raw)
	if err != nil {
		t.Fatalf("normalizeTofuOutputs: %v", err)
	}
	var outputs map[string]tofuOutput
	if err := json.Unmarshal([]byte(got), &outputs); err != nil {
		t.Fatalf("canonical form does not parse back: %v", err)
	}
	if len(outputs) != 3 {
		t.Fatalf("outputs = %d, want 3", len(outputs))
	}
	if string(outputs["namespace"].Value) != `"customer-a"` || outputs["namespace"].Sensitive {
		t.Errorf("namespace = %+v, want non-sensitive \"customer-a\"", outputs["namespace"])
	}
	if !outputs["db_password"].Sensitive {
		t.Error("db_password must keep its sensitive flag")
	}
	if string(outputs["subnet_ids"].Value) != `["a","b"]` {
		t.Errorf("subnet_ids value = %s, want the list to survive as raw JSON", outputs["subnet_ids"].Value)
	}

	// Nothing captured (an empty file — `tofu output` failed) leaves the
	// column alone; a genuinely empty result is recorded canonically.
	for name, in := range map[string][]byte{"nil": nil, "empty": []byte(""), "blank": []byte("\n")} {
		if got, err := normalizeTofuOutputs(in); err != nil || got != "" {
			t.Errorf("%s: = (%q, %v), want (\"\", nil)", name, got, err)
		}
	}
	if got, err := normalizeTofuOutputs([]byte("{}")); err != nil || got != "{}" {
		t.Errorf("empty object: = (%q, %v), want (\"{}\", nil)", got, err)
	}

	for name, in := range map[string][]byte{
		"not json":    []byte("not json"),
		"wrong shape": []byte(`{"namespace": "customer-a"}`),
	} {
		if _, err := normalizeTofuOutputs(in); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// TestNormalizeTofuResources: a valid inventory round-trips (module-qualified
// addresses and raw ids intact), records without an address are dropped,
// nothing-captured yields "" (no column write) while an empty inventory is
// recorded as "[]" (the state after a destroy), and a non-array shape — e.g. the
// outputs object landing under the wrong key — is an error, never persisted.
func TestNormalizeTofuResources(t *testing.T) {
	t.Parallel()

	got, err := normalizeTofuResources([]byte(`[{"address":"module.net.aws_vpc.main","mode":"managed","type":"aws_vpc","name":"main","provider":"registry.opentofu.org/hashicorp/aws","id":"vpc-1"},{"address":"","type":"orphan"},{"address":"data.aws_ami.x","mode":"data","type":"aws_ami","name":"x","provider":"p","id":null}]`))
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	var back []tofuResource
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if len(back) != 2 || back[0].Address != "module.net.aws_vpc.main" || string(back[0].ID) != `"vpc-1"` || string(back[1].ID) != "null" {
		t.Errorf("normalized = %s", got)
	}
	for _, none := range [][]byte{nil, []byte(""), []byte(" \n")} {
		if got, err := normalizeTofuResources(none); err != nil || got != "" {
			t.Errorf("%q: got %q, %v; want nothing to write", none, got, err)
		}
	}
	for _, empty := range [][]byte{[]byte(`[]`), []byte(`[{"address":""}]`)} {
		if got, err := normalizeTofuResources(empty); err != nil || got != "[]" {
			t.Errorf("%q: got %q, %v; want the canonical empty inventory", empty, got, err)
		}
	}
	if _, err := normalizeTofuResources([]byte(`{"vpc_id":{"value":"x"}}`)); err == nil {
		t.Error("an object (outputs shape) must be rejected")
	}
}

// TestNoChangeCompletes: a succeeded OpenTofu plan unit completes its apply
// unit only on a deploy or uninstall, and only when OpenTofu's verdict marker
// and the parsed plan text both say there is nothing to apply. Anything
// missing or disagreeing leaves the apply (and its gate) alone.
func TestNoChangeCompletes(t *testing.T) {
	t.Parallel()

	tfID, helmID := uuid.New(), uuid.New()
	tf := GraphNode{ID: tfID, ComponentID: tfID, Name: "infra", Type: TypeTerraform, Config: map[string]string{terraformConfigBackend: "s3"}, RequiresApproval: true}
	helmNode := GraphNode{ID: helmID, ComponentID: helmID, Name: "web", Type: TypeHelm, DependsOn: []uuid.UUID{tfID}}
	byID := map[uuid.UUID]GraphNode{}
	for _, n := range expandExecutionNodes([]GraphNode{tf, helmNode}, ActionDeploy) {
		byID[n.ID] = n
	}
	plan, apply := byID[tfID], byID[deriveApplyID(tfID)]

	const noChanges = "No changes. Your infrastructure matches the configuration.\n\nSaved the plan to: tfplan\nsecret/x configured\nSPACEFLEET_PLAN_CHANGES=false\n"
	const changes = "OpenTofu will perform the following actions:\n\n  # aws_s3_bucket.b will be created\n  + resource \"aws_s3_bucket\" \"b\" {\n    }\n\nPlan: 1 to add, 0 to change, 0 to destroy.\nSPACEFLEET_PLAN_CHANGES=true\n"

	cases := []struct {
		name   string
		action string
		node   GraphNode
		logs   string
		want   bool
	}{
		{"deploy, no changes", ActionDeploy, plan, noChanges, true},
		{"uninstall, no changes", ActionUninstall, plan, "No changes. No objects need to be destroyed.\nSPACEFLEET_PLAN_CHANGES=false\n", true},
		{"changes", ActionDeploy, plan, changes, false},
		{"no verdict (logs from before the marker)", ActionDeploy, plan, "No changes. Your infrastructure matches the configuration.\n", false},
		{"no logs captured", ActionDeploy, plan, "", false},
		{"verdict disagrees with the plan text", ActionDeploy, plan, strings.Replace(changes, "=true", "=false", 1), false},
		{"text disagrees with the verdict", ActionDeploy, plan, strings.Replace(noChanges, "=false", "=true", 1), false},
		{"apply unit is not a plan", ActionDeploy, apply, noChanges, false},
		{"non-tofu node", ActionDeploy, byID[helmID], noChanges, false},
		{"preview", ActionPreview, plan, noChanges, false},
		{"drift", ActionDrift, plan, noChanges, false},
	}
	for _, tc := range cases {
		got := noChangeCompletes(tc.action, tc.node, tc.logs, byID)
		if tc.want {
			if len(got) != 1 || got[0] != apply.ID {
				t.Errorf("%s: completes = %v, want [%s]", tc.name, got, apply.ID)
			}
		} else if got != nil {
			t.Errorf("%s: completes = %v, want none", tc.name, got)
		}
	}

	// A plan unit whose apply unit is missing from the run completes nothing.
	delete(byID, apply.ID)
	if got := noChangeCompletes(ActionDeploy, plan, noChanges, byID); got != nil {
		t.Errorf("without an apply unit: completes = %v, want none", got)
	}
}

func TestNothingToApplyMessage(t *testing.T) {
	t.Parallel()
	if got := nothingToApplyMessage(GraphNode{RequiresApproval: true}); !strings.Contains(got, "no approval was needed") {
		t.Errorf("gated message = %q", got)
	}
	if got := nothingToApplyMessage(GraphNode{}); strings.Contains(got, "approval") {
		t.Errorf("ungated message = %q", got)
	}
}
