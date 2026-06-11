package api

import (
	"testing"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/tektoninstallation"
	"github.com/spacefleet/spacefleet/lib/tekton"
)

// installedRow is a minimal stored row for the mapper tests; the interesting
// variation lives in the live presence.
func installedRow() *ent.TektonInstallation {
	return &ent.TektonInstallation{
		Enabled:          true,
		Status:           tektoninstallation.StatusInstalled,
		InstalledVersion: tekton.PinnedVersion,
	}
}

// TestToAPITektonStatusUpdateAvailable: a managed install reads as
// update-available when its stamped manifest revision differs from this
// binary's — the path that lets a footprint change at the same pinned Tekton
// version (or an install predating revision stamping) be synced.
func TestToAPITektonStatusUpdateAvailable(t *testing.T) {
	current := tekton.ManifestRevision()
	if current == "" {
		t.Fatal("ManifestRevision is empty")
	}
	cases := []struct {
		name string
		p    *tekton.Presence
		want bool
	}{
		{"in sync", &tekton.Presence{Installed: true, ControllerReady: true, Managed: true, Version: tekton.PinnedVersion, Revision: current}, false},
		{"stale revision, same version", &tekton.Presence{Installed: true, ControllerReady: true, Managed: true, Version: tekton.PinnedVersion, Revision: "000000000000"}, true},
		{"predates revision stamping", &tekton.Presence{Installed: true, ControllerReady: true, Managed: true, Version: tekton.PinnedVersion}, true},
		{"older version", &tekton.Presence{Installed: true, ControllerReady: true, Managed: true, Version: "v0.1.0", Revision: current}, true},
		{"unmanaged never offered", &tekton.Presence{Installed: true, ControllerReady: true, Managed: false, Version: "v0.1.0"}, false},
		{"not installed", &tekton.Presence{Installed: false, Managed: false}, false},
		{"managed but controller not ready still repairable", &tekton.Presence{Installed: true, ControllerReady: false, Managed: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toAPITektonStatus(installedRow(), tc.p)
			if got.UpdateAvailable != tc.want {
				t.Errorf("UpdateAvailable = %v, want %v", got.UpdateAvailable, tc.want)
			}
			if got.ExpectedRevision != current {
				t.Errorf("ExpectedRevision = %q, want %q", got.ExpectedRevision, current)
			}
		})
	}
}

// TestToAPITektonStatusStoredOnly: enable/disable responses (nil presence)
// carry stored state only and never claim an update is available.
func TestToAPITektonStatusStoredOnly(t *testing.T) {
	got := toAPITektonStatus(installedRow(), nil)
	if got.UpdateAvailable {
		t.Error("UpdateAvailable = true for a stored-only response")
	}
	if got.DetectedRevision != nil {
		t.Errorf("DetectedRevision = %v, want nil", *got.DetectedRevision)
	}
	if got.ExpectedRevision == "" {
		t.Error("ExpectedRevision missing from a stored-only response")
	}
}
