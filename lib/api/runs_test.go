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
	out := toAPIComponentRunDetail(cr)

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
	out := toAPIComponentRunDetail(cr)

	if out.Diff != nil {
		t.Errorf("non-preview run should have no diff, got %q", *out.Diff)
	}
	if out.HasChanges != nil {
		t.Errorf("non-preview run should have no has_changes, got %v", *out.HasChanges)
	}
}
