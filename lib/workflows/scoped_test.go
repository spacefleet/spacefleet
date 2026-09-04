package workflows

import (
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
)

// TestScopedSnapshot proves a component-scoped run's snapshot is the
// component's usual plan + apply pair and nothing else: the authored
// dependencies dropped, the targets folded into the plan flags after the
// authored ones, the gate forced on for a destroy but the component's own
// for a deploy, and the unit ids the ones a whole-workflow run would use.
func TestScopedSnapshot(t *testing.T) {
	t.Parallel()
	inst := uuid.New()
	c := &ent.Component{
		ID: uuid.New(), Name: "infra", Type: "terraform",
		Config:               map[string]string{"repo_url": "r", "path": "p", terraformConfigBackend: "s3", terraformConfigPlanFlags: `["-var=env=prod"]`},
		RequiresApproval:     false,
		ContinueOnFailure:    true,
		DependsOn:            []uuid.UUID{uuid.New()},
		GithubInstallationID: inst,
	}

	snap := scopedSnapshot(c, ActionUninstall, []string{"aws_instance.web", `module.vpc["a"].aws_subnet.b[0]`})
	if len(snap.Nodes) != 2 || len(snap.Groups) != 0 {
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
	if plan.Config[terraformConfigCommand] != terraformCommandPlan || apply.Config[terraformConfigCommand] != terraformCommandApply {
		t.Errorf("commands = %q / %q", plan.Config[terraformConfigCommand], apply.Config[terraformConfigCommand])
	}
	if len(plan.DependsOn) != 0 || len(apply.DependsOn) != 1 || apply.DependsOn[0] != c.ID {
		t.Errorf("deps: plan=%v apply=%v", plan.DependsOn, apply.DependsOn)
	}
	if plan.RequiresApproval || !apply.RequiresApproval {
		t.Error("a destroy must gate its apply regardless of the component's flag")
	}
	if apply.GitHubInstallationID == nil || *apply.GitHubInstallationID != inst {
		t.Errorf("installation not carried: %+v", apply.GitHubInstallationID)
	}
	if c.Config[terraformConfigPlanFlags] != `["-var=env=prod"]` {
		t.Error("scopedSnapshot mutated the component's config")
	}

	// A deploy keeps the component's own gate; no targets leaves the flags alone.
	snap = scopedSnapshot(c, ActionDeploy, nil)
	if len(snap.Nodes) != 2 || snap.Nodes[1].RequiresApproval {
		t.Errorf("deploy snapshot = %+v, want an ungated apply", snap.Nodes)
	}
	if snap.Nodes[0].Config[terraformConfigPlanFlags] != `["-var=env=prod"]` {
		t.Errorf("deploy without targets changed plan flags: %s", snap.Nodes[0].Config[terraformConfigPlanFlags])
	}
	c.RequiresApproval = true
	if snap = scopedSnapshot(c, ActionDeploy, nil); !snap.Nodes[1].RequiresApproval {
		t.Error("a deploy of a gated component must stay gated")
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
