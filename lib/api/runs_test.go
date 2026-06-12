package api

import (
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/helm"
)

// TestComponentRunDetailParsesPreviewDiff verifies the detail mapper slices the
// preview diff out of the captured logs via helm.ParseDiff and populates the
// diff / has_changes fields — the additive surface Phase 6's UI consumes.
func TestComponentRunDetailParsesPreviewDiff(t *testing.T) {
	t.Parallel()

	logs := "cloning...\n" +
		helm.DiffBeginMarker + "\n" +
		"- old\n+ new\n" +
		helm.DiffEndMarker + "\n" +
		helm.DiffChangesPrefix + "true\n"

	cr := &ent.ComponentRun{
		ID:     uuid.New(),
		Status: "succeeded",
		Logs:   logs,
	}
	out := toAPIComponentRunDetail(cr, true)

	if out.Logs == nil || *out.Logs != logs {
		t.Errorf("logs field not preserved")
	}
	if out.Diff == nil {
		t.Fatal("expected diff to be populated for a preview run")
	}
	if *out.Diff != "- old\n+ new" {
		t.Errorf("diff body = %q, want the lines between the sentinels", *out.Diff)
	}
	if out.HasChanges == nil || !*out.HasChanges {
		t.Errorf("expected has_changes=true")
	}
}

// TestComponentRunDetailNonPreviewHasNoDiff verifies a non-preview run (no diff
// sentinels in its logs) yields no diff / has_changes — the fields are omitted,
// not falsely populated.
func TestComponentRunDetailNonPreviewHasNoDiff(t *testing.T) {
	t.Parallel()

	cr := &ent.ComponentRun{
		ID:     uuid.New(),
		Status: "succeeded",
		Logs:   "Release \"web\" has been upgraded. Happy Helming!\n",
	}
	out := toAPIComponentRunDetail(cr, true)

	if out.Diff != nil {
		t.Errorf("non-preview run should have no diff, got %q", *out.Diff)
	}
	if out.HasChanges != nil {
		t.Errorf("non-preview run should have no has_changes, got %v", *out.HasChanges)
	}
}

// TestComponentRunDetailRedactsBelowEditor verifies that a viewer (canSee=false)
// gets neither the captured logs nor the parsed diff — both can echo the
// component's rendered values / applied manifests, the same secret-bearing
// free-text the snapshot graph and component config redact. has_changes is a
// non-secret boolean and is still surfaced.
func TestComponentRunDetailRedactsBelowEditor(t *testing.T) {
	t.Parallel()

	logs := "cloning...\n" +
		helm.DiffBeginMarker + "\n" +
		"- old\n+ new\n" +
		helm.DiffEndMarker + "\n" +
		helm.DiffChangesPrefix + "true\n"

	cr := &ent.ComponentRun{
		ID:     uuid.New(),
		Status: "succeeded",
		Logs:   logs,
	}
	out := toAPIComponentRunDetail(cr, false)

	if out.Logs != nil {
		t.Errorf("logs should be withheld below editor, got %q", *out.Logs)
	}
	if out.Diff != nil {
		t.Errorf("diff should be withheld below editor, got %q", *out.Diff)
	}
	if out.HasChanges == nil || !*out.HasChanges {
		t.Errorf("has_changes is non-secret and should still be surfaced")
	}
}

// storedOutputs is a component_runs.outputs column value with one plain and one
// sensitive output, in tofu's `output -json` shape.
const storedOutputs = `{
	"namespace": {"sensitive": false, "type": "string", "value": "customer-a"},
	"db_password": {"sensitive": true, "type": "string", "value": "hunter2"},
	"subnet_ids": {"sensitive": false, "type": ["list", "string"], "value": ["a", "b"]}
}`

// TestComponentRunOutputsForEditor: an editor (canSee=true) sees every captured
// output value, sensitive included, with non-string values surviving as their
// JSON shapes.
func TestComponentRunOutputsForEditor(t *testing.T) {
	t.Parallel()

	cr := &ent.ComponentRun{ID: uuid.New(), Status: "succeeded", Outputs: storedOutputs}
	out := toAPIComponentRun(cr, true)
	if out.Outputs == nil {
		t.Fatal("expected outputs to be populated")
	}
	got := *out.Outputs
	if v, ok := got["namespace"].Value.(string); !ok || v != "customer-a" {
		t.Errorf("namespace = %#v, want \"customer-a\"", got["namespace"].Value)
	}
	if v, ok := got["db_password"].Value.(string); !ok || v != "hunter2" || !got["db_password"].Sensitive {
		t.Errorf("db_password = %#v (sensitive=%v), want the value visible to an editor", got["db_password"].Value, got["db_password"].Sensitive)
	}
	if v, ok := got["subnet_ids"].Value.([]interface{}); !ok || len(v) != 2 {
		t.Errorf("subnet_ids = %#v, want a two-element list", got["subnet_ids"].Value)
	}

	// The detail mapper carries the same outputs through.
	detail := toAPIComponentRunDetail(cr, true)
	if detail.Outputs == nil || len(*detail.Outputs) != 3 {
		t.Errorf("detail outputs = %v, want the same 3 entries", detail.Outputs)
	}
}

// TestComponentRunOutputsRedactedBelowEditor: a viewer (canSee=false) still
// sees that each output exists — name and sensitive flag — but a sensitive
// output's value is dropped, mirroring the inline-values redaction rule.
func TestComponentRunOutputsRedactedBelowEditor(t *testing.T) {
	t.Parallel()

	cr := &ent.ComponentRun{ID: uuid.New(), Status: "succeeded", Outputs: storedOutputs}
	out := toAPIComponentRun(cr, false)
	if out.Outputs == nil {
		t.Fatal("expected outputs to be present (only sensitive values are dropped)")
	}
	got := *out.Outputs
	if v, ok := got["namespace"].Value.(string); !ok || v != "customer-a" {
		t.Errorf("non-sensitive namespace = %#v, want \"customer-a\"", got["namespace"].Value)
	}
	if got["db_password"].Value != nil {
		t.Errorf("sensitive value must be dropped below editor, got %#v", got["db_password"].Value)
	}
	if !got["db_password"].Sensitive {
		t.Error("the sensitive flag itself must survive so the UI can mask the entry")
	}
}

// TestComponentRunOutputsEmptyAndUnparseable: an empty column omits the field;
// a column that doesn't parse is withheld entirely (never passed through raw).
func TestComponentRunOutputsEmptyAndUnparseable(t *testing.T) {
	t.Parallel()

	if out := toAPIComponentRun(&ent.ComponentRun{ID: uuid.New(), Status: "succeeded"}, true); out.Outputs != nil {
		t.Errorf("empty column should omit outputs, got %v", out.Outputs)
	}
	cr := &ent.ComponentRun{ID: uuid.New(), Status: "succeeded", Outputs: "not json"}
	if out := toAPIComponentRun(cr, true); out.Outputs != nil {
		t.Errorf("unparseable column should be withheld, got %v", out.Outputs)
	}
}
