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
