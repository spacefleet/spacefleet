package workflows

import (
	"strings"
	"testing"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/component"
	"github.com/spacefleet/spacefleet/lib/helm"
)

func TestRepoMatches(t *testing.T) {
	t.Parallel()
	yes := []string{
		"https://github.com/acme/infra",
		"https://github.com/acme/infra.git",
		"https://github.com/Acme/Infra.git/",
		"git@github.com:acme/infra.git",
		"ssh://git@github.com/acme/infra",
		"https://x-access-token:tok@github.com/acme/infra.git",
	}
	for _, u := range yes {
		if !RepoMatches(u, "acme/infra") {
			t.Errorf("%q should match acme/infra", u)
		}
	}
	no := []string{
		"https://github.com/acme/infra-2",
		"https://gitlab.com/acme/infra",
		"https://github.com/acme",
		"",
		"not a url",
		"github.com/acme/infra",
	}
	for _, u := range no {
		if RepoMatches(u, "acme/infra") {
			t.Errorf("%q should not match acme/infra", u)
		}
	}
	if RepoMatches("https://github.com/acme/infra", "") {
		t.Error("empty full name must not match")
	}
}

// TestComponentTracks covers source + branch matching per component type:
// a git helm chart, a manifest, and an OpenTofu module take part; an OCI
// helm chart does not; an explicit git_ref tracks that branch only, and no
// git_ref tracks the default branch.
func TestComponentTracks(t *testing.T) {
	t.Parallel()
	comp := func(typ string, cfg map[string]string) *ent.Component {
		return &ent.Component{Type: component.Type(typ), Config: cfg}
	}
	repo := "https://github.com/acme/infra.git"
	cases := []struct {
		name string
		c    *ent.Component
		want bool
	}{
		{"tofu default branch", comp(TypeTerraform, map[string]string{"repo_url": repo}), true},
		{"tofu explicit branch", comp(TypeTerraform, map[string]string{"repo_url": repo, "git_ref": "main"}), true},
		{"tofu other branch", comp(TypeTerraform, map[string]string{"repo_url": repo, "git_ref": "release"}), false},
		{"manifest", comp(TypeManifest, map[string]string{"repo_url": repo}), true},
		{"helm git", comp(TypeHelm, map[string]string{"chart_source": helm.SourceGit, "repo_url": repo}), true},
		{"helm oci", comp(TypeHelm, map[string]string{"chart_source": helm.SourceOCI, "repo_url": repo}), false},
		{"other repo", comp(TypeTerraform, map[string]string{"repo_url": "https://github.com/acme/other"}), false},
	}
	for _, tc := range cases {
		if got := componentTracks(tc.c, "acme/infra", "main", "main"); got != tc.want {
			t.Errorf("%s: tracks = %v, want %v", tc.name, got, tc.want)
		}
	}
	// No git_ref tracks only the default branch.
	c := comp(TypeTerraform, map[string]string{"repo_url": repo})
	if componentTracks(c, "acme/infra", "feature", "main") {
		t.Error("no git_ref must not track a non-default branch")
	}
	if componentTracks(c, "acme/infra", "", "") {
		t.Error("an empty branch never matches")
	}
}

func TestWithGitRef(t *testing.T) {
	t.Parallel()
	in := map[string]string{"repo_url": "r", "git_ref": "main"}
	out := withGitRef(in, "feature/x")
	if out["git_ref"] != "feature/x" || out["repo_url"] != "r" || in["git_ref"] != "main" {
		t.Errorf("withGitRef = %v (input now %v)", out, in)
	}
}

func TestTriggerOf(t *testing.T) {
	t.Parallel()
	if _, ok, err := TriggerOf(&ent.WorkflowRun{}); ok || err != nil {
		t.Errorf("no trigger: ok=%v err=%v", ok, err)
	}
	tr, ok, err := TriggerOf(&ent.WorkflowRun{Trigger: `{"source":"github","event":"pull_request","installation_id":42,"repo":"acme/infra","branch":"feature/x","sha":"abc","sender":"kyle","pr_number":12,"check_run_id":777}`})
	if err != nil || !ok || tr.PRNumber != 12 || tr.CheckRunID != 777 || tr.InstallationID != 42 || tr.Repo != "acme/infra" {
		t.Errorf("trigger = %+v ok=%v err=%v", tr, ok, err)
	}
	if _, _, err := TriggerOf(&ent.WorkflowRun{Trigger: "{"}); err == nil {
		t.Error("bad JSON: want an error")
	}
}

func TestValidPushTrigger(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"", "preview", "deploy"} {
		if !ValidPushTrigger(v) {
			t.Errorf("%q should be valid", v)
		}
	}
	for _, v := range []string{"drift", "uninstall", "Deploy"} {
		if ValidPushTrigger(v) {
			t.Errorf("%q should be invalid", v)
		}
	}
}

// TestCheckSummary proves the check run's title totals the OpenTofu plan
// counts across steps and the summary table lists every step with its
// result and (for a plan step) its counts.
func TestCheckSummary(t *testing.T) {
	t.Parallel()
	planLogs := "OpenTofu will perform the following actions:\n\n  # aws_instance.web will be created\n  + resource \"aws_instance\" \"web\" {\n    }\n\nPlan: 1 to add, 0 to change, 0 to destroy.\n"
	run := &ent.WorkflowRun{Status: "succeeded"}
	steps := []*ent.ComponentRun{
		{Name: "infra · plan", Type: TypeTerraform, Status: "succeeded", Logs: planLogs},
		{Name: "api", Type: TypeHelm, Status: "succeeded", Logs: "helm diff"},
		{Name: "other · plan", Type: TypeTerraform, Status: "failed", Logs: "error"},
	}
	title, summary := checkSummary(run, steps)
	if title != "Preview succeeded: 1 to add, 0 to change, 0 to destroy" {
		t.Errorf("title = %q", title)
	}
	for _, want := range []string{
		"| infra · plan | succeeded | 1 to add, 0 to change, 0 to destroy |",
		"| api | succeeded | — |",
		"| other · plan | failed | — |",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, summary)
		}
	}
	title, _ = checkSummary(&ent.WorkflowRun{Status: "failed"}, nil)
	if title != "Preview failed" {
		t.Errorf("no-plan title = %q", title)
	}
	if got := planCounts(0, 0, 0, 0); got != "no changes" {
		t.Errorf("planCounts zero = %q", got)
	}
	if got := planCounts(1, 2, 3, 4); got != "1 to add, 2 to change, 3 to destroy, 4 to replace" {
		t.Errorf("planCounts = %q", got)
	}
}
