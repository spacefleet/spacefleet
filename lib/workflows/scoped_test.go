package workflows

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
)

// TestScopedSnapshot proves a component-scoped run's snapshot is the
// component's usual plan + apply pair and nothing else: the authored
// dependencies dropped, the targets folded into the plan flags after the
// authored ones, a run's git ref in place of the component's, the gate
// forced on for a destroy but the component's own for a deploy, and the unit
// ids the ones a whole-workflow run would use.
func TestScopedSnapshot(t *testing.T) {
	t.Parallel()
	inst := uuid.New()
	c := &ent.Component{
		ID: uuid.New(), Name: "infra", Type: "terraform",
		Config:               map[string]string{"repo_url": "r", "path": "p", "git_ref": "main", terraformConfigBackend: "s3", terraformConfigPlanFlags: `["-var=env=prod"]`},
		RequiresApproval:     false,
		ContinueOnFailure:    true,
		GithubInstallationID: inst,
		Edges:                ent.ComponentEdges{Stage: &ent.WorkflowStage{ID: uuid.New(), Name: "infra"}},
	}

	snap := scopedSnapshot(c, ActionUninstall, ScopedRunOptions{
		Targets: []string{"aws_instance.web", `module.vpc["a"].aws_subnet.b[0]`},
		GitRef:  "0123456789abcdef0123456789abcdef01234567",
	})
	if len(snap.Nodes) != 2 || len(snap.Stages) != 1 {
		t.Fatalf("snapshot = %+v, want plan + apply units", snap)
	}
	plan, apply := snap.Nodes[0], snap.Nodes[1]
	if plan.ID != c.ID || plan.ComponentID != c.ID || apply.ID != deriveApplyID(c.ID) || apply.ComponentID != c.ID {
		t.Errorf("unit ids = %s / %s, want authored + derived", plan.ID, apply.ID)
	}
	if plan.Name != "infra · plan" || apply.Name != "infra · apply" {
		t.Errorf("names = %q / %q", plan.Name, apply.Name)
	}
	wantFlags := `["-var=env=prod","-target=aws_instance.web","-target=module.vpc[\"a\"].aws_subnet.b[0]"]`
	if plan.Config[terraformConfigPlanFlags] != wantFlags {
		t.Errorf("plan flags = %s, want %s", plan.Config[terraformConfigPlanFlags], wantFlags)
	}
	if plan.Config["git_ref"] != "0123456789abcdef0123456789abcdef01234567" || apply.Config["git_ref"] != plan.Config["git_ref"] {
		t.Errorf("git_ref = %q / %q, want the run's ref on both units", plan.Config["git_ref"], apply.Config["git_ref"])
	}
	if plan.Config[terraformConfigCommand] != terraformCommandPlan || apply.Config[terraformConfigCommand] != terraformCommandApply {
		t.Errorf("commands = %q / %q", plan.Config[terraformConfigCommand], apply.Config[terraformConfigCommand])
	}
	if len(plan.DependsOn) != 0 || len(apply.DependsOn) != 1 || apply.DependsOn[0] != c.ID {
		t.Errorf("deps: plan=%v apply=%v", plan.DependsOn, apply.DependsOn)
	}
	if plan.StageID == nil || apply.StageID == nil || *plan.StageID != c.Edges.Stage.ID || *apply.StageID != c.Edges.Stage.ID || snap.Stages[0].Name != "infra" {
		t.Errorf("stage not carried: plan=%v apply=%v stages=%+v", plan.StageID, apply.StageID, snap.Stages)
	}
	if plan.RequiresApproval || !apply.RequiresApproval {
		t.Error("a destroy must gate its apply regardless of the component's flag")
	}
	if apply.GitHubInstallationID == nil || *apply.GitHubInstallationID != inst {
		t.Errorf("installation not carried: %+v", apply.GitHubInstallationID)
	}
	if c.Config[terraformConfigPlanFlags] != `["-var=env=prod"]` || c.Config["git_ref"] != "main" {
		t.Error("scopedSnapshot mutated the component's config")
	}

	// A deploy keeps the component's own gate; no targets leaves the flags
	// alone, and no ref keeps the component's own.
	snap = scopedSnapshot(c, ActionDeploy, ScopedRunOptions{})
	if len(snap.Nodes) != 2 || snap.Nodes[1].RequiresApproval {
		t.Errorf("deploy snapshot = %+v, want an ungated apply", snap.Nodes)
	}
	if snap.Nodes[0].Config[terraformConfigPlanFlags] != `["-var=env=prod"]` {
		t.Errorf("deploy without targets changed plan flags: %s", snap.Nodes[0].Config[terraformConfigPlanFlags])
	}
	if snap.Nodes[0].Config["git_ref"] != "main" {
		t.Errorf("deploy without a ref changed git_ref: %q", snap.Nodes[0].Config["git_ref"])
	}
	c.RequiresApproval = true
	if snap = scopedSnapshot(c, ActionDeploy, ScopedRunOptions{}); !snap.Nodes[1].RequiresApproval {
		t.Error("a deploy of a gated component must stay gated")
	}
}

// TestValidateGitRef accepts what can name a branch, tag, or commit and
// refuses what git itself would (check-ref-format), plus a leading "-".
func TestValidateGitRef(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"main", "release/1.2", "v1.2.0", "feature/foo-bar_baz", "0123456789abcdef0123456789abcdef01234567", "abc1234"} {
		if err := ValidateGitRef(ok); err != nil {
			t.Errorf("ValidateGitRef(%q) = %v, want ok", ok, err)
		}
	}
	for _, bad := range []string{"", "-x", "--upload-pack=evil", "a b", "a..b", "a/", "/a", "a//b", "a.lock", "a.", "a/.b", "a~1", "a^", "a:b", "a?", "a*", "a[", `a\b`, "a@{1}", "@", "a\tb", strings.Repeat("a", 256)} {
		if err := ValidateGitRef(bad); !errors.Is(err, ErrInvalidGitRef) {
			t.Errorf("ValidateGitRef(%q) = %v, want ErrInvalidGitRef", bad, err)
		}
	}
}

// TestWithTargets covers the plan-flag fold: no existing flags, existing
// flags, and unparseable existing flags (replaced, not failed).
func TestWithTargets(t *testing.T) {
	t.Parallel()
	targets := []string{"aws_instance.web"}
	cases := map[string]string{
		"":                `["-target=aws_instance.web"]`,
		`[]`:              `["-target=aws_instance.web"]`,
		`["-var=a=b"]`:    `["-var=a=b","-target=aws_instance.web"]`,
		`not json`:        `["-target=aws_instance.web"]`,
		`["-target=x.y"]`: `["-target=x.y","-target=aws_instance.web"]`,
	}
	for in, want := range cases {
		cfg := map[string]string{"repo_url": "r"}
		if in != "" {
			cfg[terraformConfigPlanFlags] = in
		}
		got := withTargets(cfg, targets)
		if got[terraformConfigPlanFlags] != want || got["repo_url"] != "r" {
			t.Errorf("withTargets(%q) = %v, want plan_flags %s", in, got, want)
		}
	}
}

// TestScopeOf proves the run-row decode: nothing for other actions or a
// whole-workflow run, the scope for a scoped run, an error for bad args.
func TestScopeOf(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	if _, ok, err := ScopeOf(&ent.WorkflowRun{Action: ActionStateOp, Args: `{"component_id":"` + id.String() + `"}`}); ok || err != nil {
		t.Errorf("state_op: ok=%v err=%v, want neither", ok, err)
	}
	if _, ok, err := ScopeOf(&ent.WorkflowRun{Action: ActionDeploy}); ok || err != nil {
		t.Errorf("whole-workflow deploy: ok=%v err=%v, want neither", ok, err)
	}
	scope, ok, err := ScopeOf(&ent.WorkflowRun{Action: ActionUninstall, Args: `{"component_id":"` + id.String() + `","component_name":"infra","targets":["a.b"]}`})
	if err != nil || !ok || scope.ComponentID != id || scope.ComponentName != "infra" || len(scope.Targets) != 1 {
		t.Errorf("scoped: scope=%+v ok=%v err=%v", scope, ok, err)
	}
	for _, args := range []string{"{", `{"targets":["a.b"]}`} {
		if _, _, err := ScopeOf(&ent.WorkflowRun{Action: ActionDeploy, Args: args}); err == nil {
			t.Errorf("args %q: want an error", args)
		}
	}
}
