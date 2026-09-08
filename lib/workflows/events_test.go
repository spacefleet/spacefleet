package workflows

import (
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/componentrun"
)

// TestBuildEvent proves an event carries the run's facts, names only the
// steps worth naming, and (for a drift event) the drifted addresses parsed
// from the plan logs.
func TestBuildEvent(t *testing.T) {
	t.Parallel()
	run := &ent.WorkflowRun{ID: uuid.New(), OrganizationID: uuid.New(), Action: "drift", Status: "succeeded", Message: "workflow succeeded", StartedBy: ""}
	app := &ent.Application{ID: uuid.New(), Name: "web"}
	driftLogs := "Note: Objects have changed outside of OpenTofu\n\nOpenTofu detected the following changes made outside of OpenTofu since the last \"tofu apply\":\n\n  # aws_instance.web has been changed\n  ~ resource \"aws_instance\" \"web\" {\n    }\n\nThis is a refresh-only plan, so OpenTofu will not take any actions to undo these.\n\nNo changes. Your infrastructure still matches the configuration.\n"
	steps := []*ent.ComponentRun{
		{Name: "infra · plan", Type: TypeTerraform, Status: componentrun.StatusSucceeded, Logs: driftLogs},
		{Name: "api", Type: TypeHelm, Status: componentrun.StatusFailed},
		{Name: "web", Type: TypeHelm, Status: componentrun.StatusSkipped},
		{Name: "ok", Type: TypeHelm, Status: componentrun.StatusSucceeded},
	}
	ev := buildEvent(EventDriftDetected, run, app, steps, "https://sf/run")
	if ev.Kind != EventDriftDetected || ev.AppName != "web" || ev.RunID != run.ID || ev.Action != "drift" || ev.RunURL != "https://sf/run" {
		t.Errorf("event = %+v", ev)
	}
	if len(ev.Steps) != 2 || ev.Steps[0].Name != "api" || ev.Steps[1].Status != "skipped" {
		t.Errorf("steps = %+v, want the failed and skipped ones", ev.Steps)
	}
	if len(ev.Drift) != 1 || ev.Drift[0] != "aws_instance.web" {
		t.Errorf("drift = %v", ev.Drift)
	}
	// A non-drift event never parses drift.
	if ev := buildEvent(EventRunFailed, run, app, steps, ""); len(ev.Drift) != 0 {
		t.Errorf("run_failed carried drift: %v", ev.Drift)
	}
	if !ValidEventKind(EventRunFailed) || ValidEventKind("test") {
		t.Error("ValidEventKind")
	}
}
