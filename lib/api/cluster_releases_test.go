package api

import (
	"testing"
	"time"

	"github.com/spacefleet/spacefleet/lib/k8s"
)

// TestToAPIRelease maps a discovered release to its API shape: required scalars
// pass through, optional empties are omitted (nil), and a zero time drops
// updated_at rather than emitting a zero date.
func TestToAPIRelease(t *testing.T) {
	updated := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	out := toAPIRelease(k8s.Release{
		Name:         "cache",
		Namespace:    "prod",
		ChartName:    "redis",
		ChartVersion: "1.2.3",
		AppVersion:   "7.0",
		Status:       "deployed",
		Revision:     2,
		Values:       "replicas: 3\n",
		Updated:      updated,
	})

	if out.Name != "cache" || out.Namespace != "prod" || out.ChartName != "redis" ||
		out.ChartVersion != "1.2.3" || out.Status != "deployed" || out.Revision != 2 {
		t.Fatalf("scalar fields not mapped: %+v", out)
	}
	if out.AppVersion == nil || *out.AppVersion != "7.0" {
		t.Errorf("app_version = %v, want 7.0", out.AppVersion)
	}
	if out.Values == nil || *out.Values != "replicas: 3\n" {
		t.Errorf("values = %v, want the release values", out.Values)
	}
	if out.UpdatedAt == nil || !out.UpdatedAt.Equal(updated) {
		t.Errorf("updated_at = %v, want %v", out.UpdatedAt, updated)
	}
}

func TestToAPIReleaseOmitsEmptyOptionals(t *testing.T) {
	out := toAPIRelease(k8s.Release{Name: "web", Namespace: "default", Status: "deployed", Revision: 1})
	if out.AppVersion != nil {
		t.Errorf("app_version = %v, want nil when empty", out.AppVersion)
	}
	if out.Values != nil {
		t.Errorf("values = %v, want nil when empty", out.Values)
	}
	if out.UpdatedAt != nil {
		t.Errorf("updated_at = %v, want nil for zero time", out.UpdatedAt)
	}
}
