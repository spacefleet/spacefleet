package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/application"
	"github.com/spacefleet/spacefleet/ent/component"
	"github.com/spacefleet/spacefleet/ent/componentrun"
	"github.com/spacefleet/spacefleet/ent/githubinstallation"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
	"github.com/spacefleet/spacefleet/lib/githubapp"
	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// Run triggers: a GitHub push to a branch one of an application's components
// tracks starts a preview or a deploy (applications.push_trigger), and a
// pull request against such a branch starts a speculative preview at the
// pull request's head (applications.pr_plans) whose outcome is reported back
// onto the pull request as a check run.
//
// Matching is by the component's attached GitHub installation and repository
// — a delivery names its installation id, and only components attached to
// that installation whose repo_url is that repository take part — so a
// webhook can never start a run for an application that did not opt in
// through a repository its own installation grants. A pull request from a
// fork is never planned: that would run a stranger's code with the
// component's credentials.

// Push trigger values (applications.push_trigger).
const (
	PushTriggerNone    = ""
	PushTriggerPreview = "preview"
	PushTriggerDeploy  = "deploy"
)

// ValidPushTrigger reports whether v is a push_trigger setting.
func ValidPushTrigger(v string) bool {
	return v == PushTriggerNone || v == PushTriggerPreview || v == PushTriggerDeploy
}

// RunTrigger is how a triggered run was started — the JSON stored on
// workflow_runs.trigger. CheckRunID is the GitHub check run a pull-request
// preview reports to (0 when none was created).
type RunTrigger struct {
	Source         string `json:"source"`
	Event          string `json:"event"`
	InstallationID int64  `json:"installation_id"`
	Repo           string `json:"repo"`
	Branch         string `json:"branch"`
	SHA            string `json:"sha"`
	Sender         string `json:"sender,omitempty"`
	PRNumber       int    `json:"pr_number,omitempty"`
	CheckRunID     int64  `json:"check_run_id,omitempty"`
}

// TriggerSourceGitHub is the only trigger source today.
const TriggerSourceGitHub = "github"

// TriggerOf decodes the trigger stored on a run; ok is false for a run a
// person or the scheduler started.
func TriggerOf(run *ent.WorkflowRun) (t RunTrigger, ok bool, err error) {
	if run.Trigger == "" {
		return RunTrigger{}, false, nil
	}
	if err := json.Unmarshal([]byte(run.Trigger), &t); err != nil {
		return RunTrigger{}, false, fmt.Errorf("workflows: run %s: decode trigger: %w", run.ID, err)
	}
	return t, true, nil
}

// CheckRunClient posts check runs to GitHub — the *githubapp.Authenticator in
// production, a fake in tests. nil on a deployment without a GitHub App.
type CheckRunClient interface {
	CreateCheckRun(ctx context.Context, installationID int64, repo string, c githubapp.CheckRun) (int64, error)
	UpdateCheckRun(ctx context.Context, installationID int64, repo string, id int64, c githubapp.CheckRun) error
}

// SetGitHubChecks wires the check-run client and the deployment's public
// base URL (for the check's link to the run page). Both the API process
// (which receives webhooks and starts runs) and the worker (which completes
// the check when a run settles) call it.
func (s *Service) SetGitHubChecks(client CheckRunClient, externalURL string) {
	s.checks = client
	s.externalURL = trimSlash(externalURL)
}

// TriggeredRun is one run a webhook delivery started, for the handler's
// response and logs.
type TriggeredRun struct {
	OrgID         uuid.UUID
	ApplicationID uuid.UUID
	RunID         uuid.UUID
	Action        string
}

// TriggerRuns acts on a push or pull_request delivery: for every application
// (in any organization) that has a component attached to the delivery's
// installation, sourced from its repository, and tracking the branch in
// question, it starts the configured run — the push_trigger action for a
// push to the tracked branch; a preview at the pull request's head, with a
// check run, for a pull request against the tracked branch when pr_plans is
// on. An application already running something is skipped (a pull request
// gets a neutral check saying so); failures are logged per application and
// the sweep continues. Each run is started exactly as a user-started one
// (BeginRun → enqueue → job id), with started_by "github:<sender>" and the
// trigger recorded on the row.
func (s *Service) TriggerRuns(ctx context.Context, ev githubapp.WebhookEvent, enqueue EnqueueRunFunc) ([]TriggeredRun, error) {
	if ev.Event == githubapp.EventPullRequest && !ev.SameRepo {
		return nil, nil
	}
	// The branch a component must track: the pushed branch, or the branch
	// the pull request targets.
	branch := ev.Branch
	if ev.Event == githubapp.EventPullRequest {
		branch = ev.BaseBranch
	}
	installs, err := s.ent.GitHubInstallation.Query().
		Where(githubinstallation.InstallationID(ev.InstallationID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	if len(installs) == 0 {
		return nil, nil
	}
	installIDs := make([]uuid.UUID, 0, len(installs))
	for _, in := range installs {
		installIDs = append(installIDs, in.ID)
	}
	comps, err := s.ent.Component.Query().
		Where(component.GithubInstallationIDIn(installIDs...)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	// The applications with at least one matching component, each once.
	matched := map[uuid.UUID]bool{}
	var appIDs []uuid.UUID
	for _, c := range comps {
		if !componentTracks(c, ev.Repo, branch, ev.DefaultBranch) {
			continue
		}
		if !matched[c.ApplicationID] {
			matched[c.ApplicationID] = true
			appIDs = append(appIDs, c.ApplicationID)
		}
	}
	if len(appIDs) == 0 {
		return nil, nil
	}
	apps, err := s.ent.Application.Query().Where(application.IDIn(appIDs...)).All(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].CreatedAt.Before(apps[j].CreatedAt) })

	var started []TriggeredRun
	for _, app := range apps {
		var action string
		switch ev.Event {
		case githubapp.EventPush:
			action = app.PushTrigger
		case githubapp.EventPullRequest:
			if app.PrPlans {
				action = ActionPreview
			}
		}
		if !ValidPushTrigger(action) || action == PushTriggerNone {
			continue
		}
		run, err := s.triggerRun(ctx, app, ev, action, enqueue)
		if err != nil {
			log.Printf("workflows: trigger %s on %s for application %s: %v", ev.Event, ev.Repo, app.ID, err)
			continue
		}
		if run != nil {
			started = append(started, TriggeredRun{OrgID: app.OrganizationID, ApplicationID: app.ID, RunID: run.ID, Action: action})
		}
	}
	return started, nil
}

// triggerRun starts one triggered run for app. A pull-request preview pins
// every component sourced from the repository to the pull request's head
// branch and opens an in-progress check run linking to the run page; the
// check's id rides on the trigger so the settle path can complete it. Returns
// (nil, nil) when the application is busy.
func (s *Service) triggerRun(ctx context.Context, app *ent.Application, ev githubapp.WebhookEvent, action string, enqueue EnqueueRunFunc) (*ent.WorkflowRun, error) {
	var adjust func([]*ent.Component)
	if ev.Event == githubapp.EventPullRequest {
		adjust = func(comps []*ent.Component) {
			for _, c := range comps {
				if componentSourcedFrom(c, ev.Repo) {
					c.Config = withGitRef(c.Config, ev.Branch)
				}
			}
		}
	}
	trigger := RunTrigger{
		Source:         TriggerSourceGitHub,
		Event:          ev.Event,
		InstallationID: ev.InstallationID,
		Repo:           ev.Repo,
		Branch:         ev.Branch,
		SHA:            ev.SHA,
		Sender:         ev.Sender,
		PRNumber:       ev.PRNumber,
	}
	run, err := s.beginRun(ctx, app.OrganizationID, app.ID, action, adjust)
	if err != nil {
		if errors.Is(err, ErrRunInFlight) {
			if ev.Event == githubapp.EventPullRequest {
				s.postCheck(ctx, trigger, 0, githubapp.CheckRun{
					Name: checkName(app), HeadSHA: ev.SHA, Status: "completed", Conclusion: githubapp.CheckNeutral,
					Title:   "Not planned: another run is in progress",
					Summary: "Spacefleet skipped this pull request because " + app.Name + " already has a run in progress. Push again once it finishes to plan.",
				})
			}
			return nil, nil
		}
		return nil, err
	}
	if ev.Event == githubapp.EventPullRequest {
		trigger.CheckRunID = s.postCheck(ctx, trigger, 0, githubapp.CheckRun{
			Name: checkName(app), HeadSHA: ev.SHA, Status: "in_progress",
			DetailsURL: s.runURL(app.ID, run.ID), ExternalID: run.ID.String(),
			Title: "Planning…", Summary: "Spacefleet is previewing this pull request.",
		})
	}
	if err := s.SetRunTrigger(ctx, app.OrganizationID, run.ID, trigger); err != nil {
		return nil, err
	}
	if ev.Sender != "" {
		if err := s.SetRunStartedBy(ctx, app.OrganizationID, run.ID, "github:"+ev.Sender); err != nil {
			return nil, err
		}
	}
	jobID, err := enqueue(ctx, WorkflowRunArgs{
		WorkflowRunID: run.ID,
		OrgID:         app.OrganizationID,
		ApplicationID: app.ID,
		Action:        action,
	})
	if err != nil {
		// Never leave the pending run arming the in-flight gate with no job
		// behind it (the same posture as StartRun and the drift scheduler).
		_ = s.MarkRun(ctx, app.OrganizationID, run.ID, "failed", "failed to enqueue triggered run: "+err.Error())
		s.CompleteTriggerCheck(ctx, app.OrganizationID, run.ID)
		return nil, err
	}
	if err := s.SetRunJob(ctx, app.OrganizationID, run.ID, jobID); err != nil {
		return nil, err
	}
	return run, nil
}

// SetRunTrigger records how a run was started (see RunTrigger).
func (s *Service) SetRunTrigger(ctx context.Context, orgID, runID uuid.UUID, t RunTrigger) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	affected, err := s.ent.WorkflowRun.Update().
		Where(workflowrun.OrganizationID(orgID), workflowrun.ID(runID)).
		SetTrigger(string(raw)).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return &ent.NotFoundError{}
	}
	return nil
}

// CompleteTriggerCheck completes the check run of a pull-request preview
// once the run is terminal: success when the preview succeeded, failure
// otherwise, with a per-step summary (plan counts for OpenTofu steps). A
// no-op for a run without a check run or on a deployment without a GitHub
// App; best-effort — every failure is logged, since the run itself has
// already settled. Called from every settle path (the worker, the reaper, a
// cancel).
func (s *Service) CompleteTriggerCheck(ctx context.Context, orgID, runID uuid.UUID) {
	if s.checks == nil {
		return
	}
	run, err := s.ent.WorkflowRun.Query().Where(workflowrun.OrganizationID(orgID), workflowrun.ID(runID)).Only(ctx)
	if err != nil {
		log.Printf("workflows: complete check for run %s: %v", runID, err)
		return
	}
	trigger, ok, err := TriggerOf(run)
	if err != nil || !ok || trigger.CheckRunID == 0 {
		return
	}
	switch run.Status {
	case "succeeded", "failed", "partial":
	default:
		return
	}
	steps, err := s.ent.ComponentRun.Query().
		Where(componentrun.OrganizationID(orgID), componentrun.WorkflowRunID(runID)).
		Order(ent.Asc(componentrun.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		log.Printf("workflows: complete check for run %s: load steps: %v", runID, err)
		return
	}
	title, summary := checkSummary(run, steps)
	conclusion := githubapp.CheckFailure
	if run.Status == "succeeded" {
		conclusion = githubapp.CheckSuccess
	}
	s.postCheck(ctx, trigger, trigger.CheckRunID, githubapp.CheckRun{
		Status: "completed", Conclusion: conclusion, DetailsURL: s.runURL(run.ApplicationID, run.ID),
		Title: title, Summary: summary,
	})
}

// postCheck creates (id == 0) or updates a check run, logging and swallowing
// failures: a check is a courtesy to the pull request, never a reason to
// fail the run. Returns the check run's id (0 on failure or without a
// client).
func (s *Service) postCheck(ctx context.Context, t RunTrigger, id int64, c githubapp.CheckRun) int64 {
	if s.checks == nil {
		return 0
	}
	if id == 0 {
		created, err := s.checks.CreateCheckRun(ctx, t.InstallationID, t.Repo, c)
		if err != nil {
			log.Printf("workflows: create check run on %s@%s: %v", t.Repo, t.SHA, err)
			return 0
		}
		return created
	}
	if err := s.checks.UpdateCheckRun(ctx, t.InstallationID, t.Repo, id, c); err != nil {
		log.Printf("workflows: update check run %d on %s: %v", id, t.Repo, err)
	}
	return id
}

// runURL is the run page's public address, or "" without an external URL.
func (s *Service) runURL(appID, runID uuid.UUID) string {
	if s.externalURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/applications/%s/runs/%s", s.externalURL, appID, runID)
}

// checkName is the check run's name on the pull request.
func checkName(app *ent.Application) string {
	return "Spacefleet · " + app.Name
}

// checkSummary renders a settled run as a check run's title and Markdown
// summary: one table row per step with its result, and for an OpenTofu plan
// step the change counts parsed from its logs. The title totals the plan
// counts across steps. A pure function (unit-testable).
func checkSummary(run *ent.WorkflowRun, steps []*ent.ComponentRun) (title, summary string) {
	var add, change, destroy, replace int
	var sawPlan bool
	var b strings.Builder
	b.WriteString("| Step | Result | Plan |\n| --- | --- | --- |\n")
	for _, cr := range steps {
		plan := "—"
		if cr.Type == TypeTerraform && cr.Logs != "" {
			if p := tofu.ParsePlan(cr.Logs); p.Found {
				sawPlan = true
				add, change, destroy, replace = add+p.Add, change+p.Change, destroy+p.Destroy, replace+p.Replace
				plan = planCounts(p.Add, p.Change, p.Destroy, p.Replace)
			}
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", strings.ReplaceAll(cr.Name, "|", "\\|"), cr.Status, plan)
	}
	verb := "Preview " + string(run.Status)
	if sawPlan {
		title = verb + ": " + planCounts(add, change, destroy, replace)
	} else {
		title = verb
	}
	return title, b.String()
}

// planCounts renders OpenTofu change counts the way a plan does ("2 to add,
// 1 to change, 0 to destroy"), with replacements when any.
func planCounts(add, change, destroy, replace int) string {
	if add+change+destroy+replace == 0 {
		return "no changes"
	}
	out := fmt.Sprintf("%d to add, %d to change, %d to destroy", add, change, destroy)
	if replace > 0 {
		out += fmt.Sprintf(", %d to replace", replace)
	}
	return out
}

// componentTracks reports whether a component is sourced from the named
// repository and tracks branch: its git_ref is that branch, or it has no
// git_ref and branch is the repository's default branch.
func componentTracks(c *ent.Component, repo, branch, defaultBranch string) bool {
	if !componentSourcedFrom(c, repo) {
		return false
	}
	ref := c.Config[helm.ConfigGitRef]
	if ref == "" {
		return branch != "" && branch == defaultBranch
	}
	return ref == branch
}

// componentSourcedFrom reports whether a component clones its source from
// the named GitHub repository: a manifest or OpenTofu component's repo_url,
// or a Helm component's when its chart source is git.
func componentSourcedFrom(c *ent.Component, repo string) bool {
	return clonesFromGit(c) && RepoMatches(c.Config[helm.ConfigRepoURL], repo)
}

// clonesFromGit reports whether a component's source is a git clone (its
// repo_url at its git_ref): a manifest or OpenTofu component, or a Helm
// component whose chart source is git.
func clonesFromGit(c *ent.Component) bool {
	switch string(c.Type) {
	case TypeHelm:
		return c.Config[helmConfigChartSource] == helm.SourceGit
	case TypeManifest, TypeTerraform:
		return true
	default:
		return false
	}
}

// RepoMatches reports whether a git URL names the GitHub repository with the
// given full name (owner/name), case-insensitively, for the https, ssh, and
// scp-like forms with or without a trailing .git.
func RepoMatches(repoURL, fullName string) bool {
	path := githubRepoPath(repoURL)
	return path != "" && strings.EqualFold(path, fullName)
}

// withGitRef returns a copy of a config map with git_ref set to ref, never
// mutating the input.
func withGitRef(cfg map[string]string, ref string) map[string]string {
	out := make(map[string]string, len(cfg)+1)
	for k, v := range cfg {
		out[k] = v
	}
	out[helm.ConfigGitRef] = ref
	return out
}
