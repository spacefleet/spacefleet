//go:build integration

package workflows

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
	"github.com/spacefleet/spacefleet/lib/githubapp"
	"github.com/spacefleet/spacefleet/lib/testsupport"
)

// fakeChecks records check-run calls.
type fakeChecks struct {
	created []githubapp.CheckRun
	updated map[int64]githubapp.CheckRun
	nextID  int64
}

func (f *fakeChecks) CreateCheckRun(_ context.Context, _ int64, _ string, c githubapp.CheckRun) (int64, error) {
	f.created = append(f.created, c)
	f.nextID++
	return f.nextID, nil
}

func (f *fakeChecks) UpdateCheckRun(_ context.Context, _ int64, _ string, id int64, c githubapp.CheckRun) error {
	if f.updated == nil {
		f.updated = map[int64]githubapp.CheckRun{}
	}
	f.updated[id] = c
	return nil
}

// TestTriggerRuns drives the webhook matching end to end against Postgres:
// a push to the tracked default branch starts the configured deploy with
// the trigger and starter recorded; a pull request while that deploy is
// pending gets a neutral check and no run; once the deploy settles, the
// pull request starts a preview pinned to its head branch with an
// in-progress check whose id rides on the trigger, and completing the run
// completes the check; pushes to other branches, unknown installations,
// fork pull requests, and applications without a trigger start nothing.
func TestTriggerRuns(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	checks := &fakeChecks{}
	svc.SetGitHubChecks(checks, "https://sf.example.com/")
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	install, err := client.GitHubInstallation.Create().SetOrganizationID(org.ID).SetInstallationID(42).Save(ctx)
	if err != nil {
		t.Fatalf("create installation: %v", err)
	}
	tf := addComponent(t, client, org.ID, app.ID, "infra", map[string]string{
		"repo_url": "https://github.com/acme/infra.git", "path": ".",
		terraformConfigBackend: "s3", terraformConfigBackendConfig: `{"bucket":"b","key":"k","region":"r"}`,
	})
	if _, err := client.Component.UpdateOneID(tf.ID).SetType(TypeTerraform).SetGithubInstallationID(install.ID).Save(ctx); err != nil {
		t.Fatalf("attach installation: %v", err)
	}
	if _, err := client.Application.UpdateOneID(app.ID).SetPushTrigger(PushTriggerDeploy).SetPrPlans(true).Save(ctx); err != nil {
		t.Fatalf("set triggers: %v", err)
	}
	// A second application on the same installation with no triggers.
	quiet := newApp(t, client, org.ID, "quiet")
	q := addComponent(t, client, org.ID, quiet.ID, "infra", map[string]string{"repo_url": "https://github.com/acme/infra", "path": "."})
	if _, err := client.Component.UpdateOneID(q.ID).SetType(TypeTerraform).SetGithubInstallationID(install.ID).Save(ctx); err != nil {
		t.Fatalf("attach installation: %v", err)
	}

	var enqueued []WorkflowRunArgs
	enqueue := func(_ context.Context, a WorkflowRunArgs) (string, error) {
		enqueued = append(enqueued, a)
		return "job-1", nil
	}
	push := githubapp.WebhookEvent{Event: githubapp.EventPush, InstallationID: 42, Repo: "acme/infra", DefaultBranch: "main", Branch: "main", SHA: "abc123", Sender: "kyle"}

	// Nothing for other branches, other installations, or another repo.
	for name, ev := range map[string]githubapp.WebhookEvent{
		"other branch":       {Event: githubapp.EventPush, InstallationID: 42, Repo: "acme/infra", DefaultBranch: "main", Branch: "feature"},
		"other installation": {Event: githubapp.EventPush, InstallationID: 7, Repo: "acme/infra", DefaultBranch: "main", Branch: "main"},
		"other repo":         {Event: githubapp.EventPush, InstallationID: 42, Repo: "acme/other", DefaultBranch: "main", Branch: "main"},
		"fork pr":            {Event: githubapp.EventPullRequest, InstallationID: 42, Repo: "acme/infra", DefaultBranch: "main", Branch: "x", BaseBranch: "main", SameRepo: false},
	} {
		started, err := svc.TriggerRuns(ctx, ev, enqueue)
		if err != nil || len(started) != 0 {
			t.Errorf("%s: started=%v err=%v, want nothing", name, started, err)
		}
	}

	started, err := svc.TriggerRuns(ctx, push, enqueue)
	if err != nil || len(started) != 1 || started[0].Action != ActionDeploy || started[0].ApplicationID != app.ID {
		t.Fatalf("push: started=%+v err=%v", started, err)
	}
	run, err := client.WorkflowRun.Get(ctx, started[0].RunID)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if run.Action != workflowrun.ActionDeploy || run.StartedBy != "github:kyle" || run.JobID != "job-1" {
		t.Errorf("run = action %s by %q job %q", run.Action, run.StartedBy, run.JobID)
	}
	tr, ok, err := TriggerOf(run)
	if err != nil || !ok || tr.Event != githubapp.EventPush || tr.SHA != "abc123" || tr.Repo != "acme/infra" || tr.CheckRunID != 0 {
		t.Errorf("trigger = %+v ok=%v err=%v", tr, ok, err)
	}
	if len(enqueued) != 1 || enqueued[0].Action != ActionDeploy || enqueued[0].WorkflowRunID != run.ID {
		t.Errorf("enqueued = %+v", enqueued)
	}

	// A pull request while the deploy is pending: neutral check, no run.
	pr := githubapp.WebhookEvent{Event: githubapp.EventPullRequest, InstallationID: 42, Repo: "acme/infra", DefaultBranch: "main", Branch: "feature/x", BaseBranch: "main", SHA: "def456", Sender: "kyle", PRNumber: 12, SameRepo: true}
	started, err = svc.TriggerRuns(ctx, pr, enqueue)
	if err != nil || len(started) != 0 {
		t.Fatalf("pr while busy: started=%+v err=%v", started, err)
	}
	if len(checks.created) != 1 || checks.created[0].Conclusion != githubapp.CheckNeutral || checks.created[0].HeadSHA != "def456" {
		t.Errorf("busy check = %+v", checks.created)
	}

	// Settle the deploy; the pull request now plans at its head.
	if err := svc.MarkRun(ctx, org.ID, run.ID, "failed", "done"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	started, err = svc.TriggerRuns(ctx, pr, enqueue)
	if err != nil || len(started) != 1 || started[0].Action != ActionPreview {
		t.Fatalf("pr: started=%+v err=%v", started, err)
	}
	preview, err := client.WorkflowRun.Get(ctx, started[0].RunID)
	if err != nil {
		t.Fatalf("load preview: %v", err)
	}
	var snap GraphSnapshot
	if err := json.Unmarshal([]byte(preview.Graph), &snap); err != nil || len(snap.Nodes) != 1 || snap.Nodes[0].Config["git_ref"] != "feature/x" {
		t.Errorf("preview snapshot = %s (err %v), want the component pinned to feature/x", preview.Graph, err)
	}
	if tf2, _ := client.Component.Get(ctx, tf.ID); tf2.Config["git_ref"] != "" {
		t.Error("the authored component's git_ref was changed")
	}
	tr, _, _ = TriggerOf(preview)
	if tr.PRNumber != 12 || tr.CheckRunID != 2 || tr.Branch != "feature/x" {
		t.Errorf("preview trigger = %+v", tr)
	}
	if c := checks.created[1]; c.Status != "in_progress" || c.ExternalID != preview.ID.String() || !strings.Contains(c.DetailsURL, "https://sf.example.com/applications/"+app.ID.String()+"/runs/"+preview.ID.String()) || c.Name != "Spacefleet · web" {
		t.Errorf("in-progress check = %+v", c)
	}

	// Completing an unsettled run is a no-op; a settled one completes the check.
	svc.CompleteTriggerCheck(ctx, org.ID, preview.ID)
	if len(checks.updated) != 0 {
		t.Errorf("completed an unsettled run: %+v", checks.updated)
	}
	if err := svc.MarkRun(ctx, org.ID, preview.ID, "succeeded", "done"); err != nil {
		t.Fatalf("settle preview: %v", err)
	}
	svc.CompleteTriggerCheck(ctx, org.ID, preview.ID)
	upd, ok := checks.updated[2]
	if !ok || upd.Status != "completed" || upd.Conclusion != githubapp.CheckSuccess || upd.Title != "Preview succeeded" || !strings.Contains(upd.Summary, "| infra · plan | pending |") {
		t.Errorf("completed check = %+v (ok=%v)", upd, ok)
	}
	// Runs without a trigger or check are left alone.
	svc.CompleteTriggerCheck(ctx, org.ID, run.ID)
	svc.CompleteTriggerCheck(ctx, org.ID, uuid.New())
	if len(checks.updated) != 1 {
		t.Errorf("updated = %+v", checks.updated)
	}

	// Cross-check: the quiet app never started anything.
	n, err := client.WorkflowRun.Query().Where(workflowrun.ApplicationID(quiet.ID)).Count(ctx)
	if err != nil || n != 0 {
		t.Errorf("quiet app runs = %d (err %v)", n, err)
	}
	_ = ent.Asc
}
