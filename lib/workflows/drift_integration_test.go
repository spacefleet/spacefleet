//go:build integration

package workflows

import (
	"context"
	"testing"
	"time"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
	"github.com/spacefleet/spacefleet/lib/testsupport"
)

// TestScheduleDueDriftChecks proves the scheduler starts a drift run only for
// an application whose schedule is due: one with an interval and no prior
// check starts (and records the enqueued job id); one whose last check is
// younger than its interval does not; one with no schedule never does; an
// application with a run already in flight is skipped (and retried later, not
// failed); and an application without an OpenTofu component is skipped
// quietly.
func TestScheduleDueDriftChecks(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	tofuApp := func(name string, interval int) *ent.Application {
		app := newApp(t, client, org.ID, name)
		tf := addComponent(t, client, org.ID, app.ID, "infra", map[string]string{
			"repo_url": "https://example.com/infra.git", "path": ".",
			terraformConfigBackend: "s3", terraformConfigBackendConfig: `{"bucket":"b","key":"k","region":"r"}`,
		})
		if _, err := client.Component.UpdateOneID(tf.ID).SetType(TypeTerraform).Save(ctx); err != nil {
			t.Fatalf("set terraform type: %v", err)
		}
		app, err := client.Application.UpdateOneID(app.ID).SetDriftIntervalMinutes(interval).Save(ctx)
		if err != nil {
			t.Fatalf("set interval: %v", err)
		}
		return app
	}

	due := tofuApp("due", 60)       // scheduled, never checked → starts
	recent := tofuApp("recent", 60) // scheduled, checked 10 min ago → waits
	off := tofuApp("off", 0)        // no schedule → never
	busy := tofuApp("busy", 60)     // scheduled, but a deploy is in flight → skipped
	helmOnly := newApp(t, client, org.ID, "helm-only")
	addComponent(t, client, org.ID, helmOnly.ID, "api", nil)
	if _, err := client.Application.UpdateOneID(helmOnly.ID).SetDriftIntervalMinutes(60).Save(ctx); err != nil {
		t.Fatalf("set interval: %v", err)
	}

	// recent: a drift run created 10 minutes ago.
	if _, err := client.WorkflowRun.Create().
		SetOrganizationID(org.ID).SetApplicationID(recent.ID).SetAction(workflowrun.ActionDrift).
		SetStatus(workflowrun.StatusSucceeded).SetCreatedAt(time.Now().Add(-10 * time.Minute)).Save(ctx); err != nil {
		t.Fatalf("seed recent drift run: %v", err)
	}
	// busy: a deploy in flight.
	if _, err := svc.BeginRun(ctx, org.ID, busy.ID, ActionDeploy); err != nil {
		t.Fatalf("begin busy deploy: %v", err)
	}

	var enqueued []WorkflowRunArgs
	enqueue := func(_ context.Context, args WorkflowRunArgs) (string, error) {
		enqueued = append(enqueued, args)
		return "job-42", nil
	}
	n, err := svc.ScheduleDueDriftChecks(ctx, enqueue)
	if err != nil {
		t.Fatalf("ScheduleDueDriftChecks: %v", err)
	}
	if n != 1 || len(enqueued) != 1 || enqueued[0].ApplicationID != due.ID || enqueued[0].Action != ActionDrift {
		t.Fatalf("started = %d, enqueued = %+v; want exactly the due app", n, enqueued)
	}

	// The due app's run exists, is a drift run, and carries the job id.
	runs, err := client.WorkflowRun.Query().Where(workflowrun.ApplicationID(due.ID)).All(ctx)
	if err != nil || len(runs) != 1 {
		t.Fatalf("due app runs = %d, %v", len(runs), err)
	}
	if runs[0].Action != workflowrun.ActionDrift || runs[0].JobID != "job-42" {
		t.Errorf("due run = action %s job %q", runs[0].Action, runs[0].JobID)
	}
	for _, app := range []*ent.Application{recent, off, helmOnly} {
		cnt, _ := client.WorkflowRun.Query().Where(workflowrun.ApplicationID(app.ID), workflowrun.ActionEQ(workflowrun.ActionDrift)).Count(ctx)
		want := 0
		if app.ID == recent.ID {
			want = 1 // only the seeded one
		}
		if cnt != want {
			t.Errorf("app %s drift runs = %d, want %d", app.Name, cnt, want)
		}
	}
	// busy keeps only its in-flight deploy — no drift run was added or failed.
	if cnt, _ := client.WorkflowRun.Query().Where(workflowrun.ApplicationID(busy.ID)).Count(ctx); cnt != 1 {
		t.Errorf("busy app runs = %d, want 1 (the deploy)", cnt)
	}

	// A second sweep right away starts nothing: the due app's check is now recent.
	if n, err := svc.ScheduleDueDriftChecks(ctx, enqueue); err != nil || n != 0 {
		t.Errorf("second sweep started %d, %v; want 0", n, err)
	}
}
