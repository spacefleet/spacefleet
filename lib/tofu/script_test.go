package tofu

import (
	"strings"
	"testing"
)

// s3Backend is the s3 state backend (the only supported type) with its required
// settings, used across the tests that don't care about backend specifics.
func s3Backend() (string, map[string]string) {
	return "s3", map[string]string{
		"bucket": "my-state",
		"key":    "prod/terraform.tfstate",
		"region": "us-east-1",
	}
}

func TestScriptPlanDeployManagedBackend(t *testing.T) {
	backend, cfg := s3Backend()
	s := Script(Apply{
		Command:       CommandPlan,
		Action:        ActionDeploy,
		RepoURL:       "https://github.com/acme/infra.git",
		Path:          "envs/prod",
		Backend:       backend,
		BackendConfig: cfg,
		Namespace:     "default",
	})
	wantContains := []string{
		"#!/bin/sh\nset -e\n",
		"git clone --depth 1 'https://github.com/acme/infra.git' /src",
		"echo \"SPACEFLEET_CHART_REVISION=$(git -C /src rev-parse HEAD)\"",
		"cd '/src/envs/prod'",
		"cat > backend_override.tf <<'EOF'",
		"backend \"s3\" {",
		"tofu init -no-color",
		"tofu plan -out=tfplan -no-color",
	}
	for _, w := range wantContains {
		if !strings.Contains(s, w) {
			t.Errorf("script missing %q\n---\n%s", w, s)
		}
	}
	// No kubeconfig is injected for a terraform component, so KUBECONFIG is never
	// exported and the kubernetes backend never appears.
	if strings.Contains(s, "KUBECONFIG") {
		t.Errorf("terraform step must not export KUBECONFIG\n---\n%s", s)
	}
	if strings.Contains(s, "backend \"kubernetes\"") || strings.Contains(s, "in_cluster_config") {
		t.Errorf("terraform must not use the kubernetes backend\n---\n%s", s)
	}
	if strings.Contains(s, "tofu apply") {
		t.Error("plan node must not apply")
	}
	if strings.Contains(s, "-destroy") || strings.Contains(s, "tofu destroy") {
		t.Error("deploy must not destroy")
	}
	if strings.Contains(s, "--branch") {
		t.Error("no git_ref set, should not pass --branch")
	}
	if strings.Contains(s, "credential.helper") {
		t.Error("no token, should not wire a credential helper")
	}
	// No PlanArtifactSecret set: the plan still produces a planfile but does not
	// store it (the read-only case), so no kubectl handover.
	if strings.Contains(s, "kubectl") || strings.Contains(s, "apk add") {
		t.Error("no PlanArtifactSecret, plan node must not store via kubectl")
	}
}

func TestScriptPlanUninstallDestroy(t *testing.T) {
	backend, cfg := s3Backend()
	s := Script(Apply{
		Command:       CommandPlan,
		Action:        ActionUninstall,
		RepoURL:       "r",
		Path:          "p",
		Backend:       backend,
		BackendConfig: cfg,
	})
	if !strings.Contains(s, "tofu plan -destroy -out=tfplan -no-color") {
		t.Errorf("uninstall plan must be a destroy plan saved to a planfile\n---\n%s", s)
	}
	if strings.Contains(s, "tofu apply") || strings.Contains(s, "tofu destroy") {
		t.Errorf("plan node must not mutate\n---\n%s", s)
	}
}

func TestScriptPlanStoresPlanfileSecret(t *testing.T) {
	// A non-preview plan node with a PlanArtifactSecret hands its saved planfile to
	// the apply node: install kubectl, then upsert the planfile into the Secret.
	backend, cfg := s3Backend()
	s := Script(Apply{
		Command:            CommandPlan,
		Action:             ActionDeploy,
		RepoURL:            "r",
		Path:               "p",
		Backend:            backend,
		BackendConfig:      cfg,
		Namespace:          "default",
		PlanArtifactSecret: "tfplan-run1-plan1",
	})
	wantContains := []string{
		"tofu plan -out=tfplan -no-color",
		"apk add --no-cache kubectl",
		"kubectl create secret generic 'tfplan-run1-plan1' --namespace 'default' --from-file=tfplan=tfplan --dry-run=client -o yaml | kubectl apply --namespace 'default' -f -",
	}
	for _, w := range wantContains {
		if !strings.Contains(s, w) {
			t.Errorf("plan store script missing %q\n---\n%s", w, s)
		}
	}
	// The plan node stores; it must not fetch, apply, or delete the Secret.
	if strings.Contains(s, "tofu apply") || strings.Contains(s, "kubectl get secret") || strings.Contains(s, "kubectl delete secret") {
		t.Errorf("plan node must only store the planfile\n---\n%s", s)
	}
	// The store must come after the plan produced the file.
	if i, j := strings.Index(s, "tofu plan -out=tfplan"), strings.Index(s, "kubectl create secret"); i < 0 || j < 0 || i >= j {
		t.Errorf("store must follow the plan (i=%d j=%d)\n---\n%s", i, j, s)
	}
}

func TestScriptApplyDeployUsesPlanfile(t *testing.T) {
	backend, cfg := s3Backend()
	s := Script(Apply{
		Command:            CommandApply,
		Action:             ActionDeploy,
		RepoURL:            "r",
		Path:               "p",
		Backend:            backend,
		BackendConfig:      cfg,
		Namespace:          "default",
		PlanArtifactSecret: "tfplan-run1-plan1",
	})
	wantContains := []string{
		"apk add --no-cache kubectl",
		"kubectl get secret 'tfplan-run1-plan1' --namespace 'default' -o 'jsonpath={.data.tfplan}' | base64 -d > tfplan",
		"tofu apply -no-color tfplan",
		"kubectl delete secret 'tfplan-run1-plan1' --namespace 'default' --ignore-not-found || true",
	}
	for _, w := range wantContains {
		if !strings.Contains(s, w) {
			t.Errorf("apply script missing %q\n---\n%s", w, s)
		}
	}
	if strings.Contains(s, "-auto-approve") {
		t.Error("apply must apply the saved planfile, not auto-approve a fresh plan")
	}
	if strings.Contains(s, "tofu plan") {
		t.Error("apply node must not run a standalone plan (it applies the saved plan)")
	}
	if strings.Contains(s, "tofu destroy") {
		t.Error("deploy apply must not destroy")
	}
	// Fetch must precede apply, which must precede the cleanup delete.
	fetch, apply, del := strings.Index(s, "kubectl get secret"), strings.Index(s, "tofu apply"), strings.Index(s, "kubectl delete secret")
	if fetch < 0 || fetch >= apply || apply >= del {
		t.Errorf("expected fetch < apply < delete (fetch=%d apply=%d del=%d)\n---\n%s", fetch, apply, del, s)
	}
}

func TestScriptApplyUninstallAppliesDestroyPlanfile(t *testing.T) {
	// An uninstall apply applies the saved DESTROY plan the plan node produced —
	// the same `tofu apply tfplan`; the destroy intent is encoded in the planfile,
	// so there is no separate `tofu destroy`.
	backend, cfg := s3Backend()
	s := Script(Apply{
		Command:            CommandApply,
		Action:             ActionUninstall,
		RepoURL:            "r",
		Path:               "p",
		Backend:            backend,
		BackendConfig:      cfg,
		Namespace:          "default",
		PlanArtifactSecret: "tfplan-run1-plan1",
	})
	if !strings.Contains(s, "tofu apply -no-color tfplan") {
		t.Errorf("uninstall apply must apply the saved destroy planfile\n---\n%s", s)
	}
	if strings.Contains(s, "tofu destroy") {
		t.Error("uninstall must apply the destroy planfile, not run tofu destroy")
	}
	if strings.Contains(s, "-auto-approve") {
		t.Error("uninstall apply must not auto-approve a fresh plan")
	}
}

func TestScriptApplyWithoutPlanfileFailsClosed(t *testing.T) {
	// Defense behind the DAG validation: an apply node with no plan artifact must
	// fail rather than silently re-plan.
	backend, cfg := s3Backend()
	s := Script(Apply{
		Command:       CommandApply,
		Action:        ActionDeploy,
		RepoURL:       "r",
		Path:          "p",
		Backend:       backend,
		BackendConfig: cfg,
	})
	if !strings.Contains(s, "exit 1") || !strings.Contains(s, "no reviewed planfile") {
		t.Errorf("apply without a planfile must fail closed\n---\n%s", s)
	}
	if strings.Contains(s, "tofu apply") {
		t.Error("apply without a planfile must not run tofu apply")
	}
}

func TestScriptPreviewIsAlwaysPlan(t *testing.T) {
	// An apply node previewed must still be a read-only plan (preview never mutates).
	backend, cfg := s3Backend()
	s := Script(Apply{
		Command:       CommandApply,
		Action:        ActionPreview,
		RepoURL:       "r",
		Path:          "p",
		Backend:       backend,
		BackendConfig: cfg,
	})
	if !strings.Contains(s, "tofu plan -no-color") {
		t.Errorf("preview must be a read-only plan\n---\n%s", s)
	}
	if strings.Contains(s, "tofu apply") || strings.Contains(s, "tofu destroy") {
		t.Errorf("preview must not mutate\n---\n%s", s)
	}
	// Preview produces no planfile and no handover (the planner leaves
	// PlanArtifactSecret empty for preview).
	if strings.Contains(s, "-out=tfplan") || strings.Contains(s, "kubectl") || strings.Contains(s, "apk add") {
		t.Errorf("preview must not save or hand over a planfile\n---\n%s", s)
	}
}

func TestScriptCustomBackend(t *testing.T) {
	s := Script(Apply{
		Command: CommandApply,
		Action:  ActionDeploy,
		RepoURL: "r",
		Path:    "p",
		Backend: "s3",
		BackendConfig: map[string]string{
			"bucket": "my-state",
			"region": "us-east-1",
			"key":    "prod/terraform.tfstate",
		},
		PlanArtifactSecret: "tfplan-run1-plan1",
	})
	wantContains := []string{
		"backend \"s3\" {",
		// Rendered in sorted key order for stable output.
		"bucket = \"my-state\"",
		"key = \"prod/terraform.tfstate\"",
		"region = \"us-east-1\"",
		"tofu apply -no-color tfplan",
	}
	for _, w := range wantContains {
		if !strings.Contains(s, w) {
			t.Errorf("custom-backend script missing %q\n---\n%s", w, s)
		}
	}
	// Custom backend must not emit any kubernetes-backend settings or KUBECONFIG.
	if strings.Contains(s, "secret_suffix") || strings.Contains(s, "in_cluster_config") || strings.Contains(s, "KUBECONFIG") {
		t.Errorf("custom backend must not emit kubernetes backend settings\n---\n%s", s)
	}
	if strings.Contains(s, "backend \"kubernetes\"") {
		t.Errorf("custom backend must not fall back to kubernetes\n---\n%s", s)
	}
	// Sorted order: bucket before key before region.
	if i, j, k := strings.Index(s, "bucket ="), strings.Index(s, "key ="), strings.Index(s, "region ="); i >= j || j >= k {
		t.Errorf("backend_config keys not in sorted order\n---\n%s", s)
	}
}

func TestScriptGitRefBranchFlag(t *testing.T) {
	withRef := Script(Apply{Command: CommandPlan, Action: ActionDeploy, RepoURL: "r", GitRef: "release-1", Path: "p", Backend: "s3"})
	if !strings.Contains(withRef, "git clone --depth 1 --branch 'release-1' 'r' /src") {
		t.Errorf("expected --branch with git_ref\n---\n%s", withRef)
	}
	noRef := Script(Apply{Command: CommandPlan, Action: ActionDeploy, RepoURL: "r", Path: "p", Backend: "s3"})
	if strings.Contains(noRef, "--branch") {
		t.Errorf("expected no --branch without git_ref\n---\n%s", noRef)
	}
}

func TestScriptTokenWiresCredentialHelper(t *testing.T) {
	withTok := Script(Apply{Command: CommandPlan, Action: ActionDeploy, RepoURL: "r", Path: "p", Backend: "s3", HasGitToken: true})
	if !strings.Contains(withTok, "git config --global credential.helper 'store --file=/workspace/creds/git-credentials'") {
		t.Errorf("expected credential helper wired when HasGitToken\n---\n%s", withTok)
	}
	// The token must never appear in the script string itself.
	if strings.Contains(withTok, "x-access-token") || strings.Contains(withTok, "@github.com") {
		t.Errorf("token material must not appear in the script\n---\n%s", withTok)
	}
	withoutTok := Script(Apply{Command: CommandPlan, Action: ActionDeploy, RepoURL: "r", Path: "p", Backend: "s3"})
	if strings.Contains(withoutTok, "credential.helper") {
		t.Errorf("no credential helper expected without a token\n---\n%s", withoutTok)
	}
}

func TestScriptTokenWiredForUninstall(t *testing.T) {
	// An uninstall (tofu destroy) still clones the root module, so the token wiring
	// is honored regardless of action.
	s := Script(Apply{Command: CommandApply, Action: ActionUninstall, RepoURL: "r", Path: "p", Backend: "s3", HasGitToken: true, PlanArtifactSecret: "tfplan-run1-plan1"})
	if !strings.Contains(s, "credential.helper 'store --file=/workspace/creds/git-credentials'") {
		t.Errorf("uninstall must still wire the credential helper to clone\n---\n%s", s)
	}
}

func TestScriptRejectsPathTraversal(t *testing.T) {
	for _, p := range []string{"../etc", "envs/../../secret", ".."} {
		s := Script(Apply{Command: CommandPlan, Action: ActionDeploy, RepoURL: "r", Path: p, Backend: "s3"})
		if !strings.Contains(s, "path traversal not allowed") {
			t.Errorf("path %q should be rejected\n---\n%s", p, s)
		}
		if strings.Contains(s, "tofu init") {
			t.Errorf("path %q rejected but init still emitted\n---\n%s", p, s)
		}
		if strings.Contains(s, "git clone") {
			t.Errorf("path %q rejected but clone still emitted\n---\n%s", p, s)
		}
	}
}

func TestScriptCloudAuthSourcesEnvFile(t *testing.T) {
	backend, cfg := s3Backend()
	s := Script(Apply{
		Command:            CommandApply,
		Action:             ActionDeploy,
		RepoURL:            "r",
		Path:               "p",
		Backend:            backend,
		BackendConfig:      cfg,
		HasCloudAuth:       true,
		PlanArtifactSecret: "tfplan-run1-plan1",
	})
	const srcLine = ". /workspace/creds/aws.env"
	if !strings.Contains(s, srcLine) {
		t.Errorf("cloud auth must source the aws env file\n---\n%s", s)
	}
	// The source line must come before tofu init so the backend authenticates.
	if i, j := strings.Index(s, srcLine), strings.Index(s, "tofu init"); i < 0 || j < 0 || i >= j {
		t.Errorf("source line must precede tofu init (i=%d j=%d)\n---\n%s", i, j, s)
	}
}

func TestScriptNoCloudAuthNoEnvFile(t *testing.T) {
	backend, cfg := s3Backend()
	s := Script(Apply{
		Command:            CommandApply,
		Action:             ActionDeploy,
		RepoURL:            "r",
		Path:               "p",
		Backend:            backend,
		BackendConfig:      cfg,
		PlanArtifactSecret: "tfplan-run1-plan1",
	})
	if strings.Contains(s, "aws.env") {
		t.Errorf("no cloud auth must not source an env file\n---\n%s", s)
	}
}

func TestScriptClusterAuthExportsKubeConfigPath(t *testing.T) {
	backend, cfg := s3Backend()
	// A plan node with a planfile secret: the case where the provider export
	// and the handover kubectl coexist in one script.
	s := Script(Apply{
		Command:            CommandPlan,
		Action:             ActionDeploy,
		RepoURL:            "r",
		Path:               "p",
		Backend:            backend,
		BackendConfig:      cfg,
		HasClusterAuth:     true,
		PlanArtifactSecret: "tfplan-run1-plan1",
	})
	const exportLine = "export KUBE_CONFIG_PATH='/workspace/creds/kubeconfig'"
	if !strings.Contains(s, exportLine) {
		t.Errorf("cluster auth must export KUBE_CONFIG_PATH\n---\n%s", s)
	}
	// The export must come before tofu init so the providers see it throughout.
	if i, j := strings.Index(s, exportLine), strings.Index(s, "tofu init"); i < 0 || j < 0 || i >= j {
		t.Errorf("export must precede tofu init (i=%d j=%d)\n---\n%s", i, j, s)
	}
	// KUBECONFIG itself must never be exported: the planfile-handover kubectl
	// calls rely on the pod's own in-cluster credentials (the pinned per-pair
	// ServiceAccount), and a global KUBECONFIG would redirect them at the auth
	// cluster instead.
	if strings.Contains(s, "KUBECONFIG") {
		t.Errorf("KUBECONFIG must not appear (it would hijack the handover kubectl)\n---\n%s", s)
	}
}

func TestScriptNoClusterAuthNoKubeconfigEnv(t *testing.T) {
	backend, cfg := s3Backend()
	s := Script(Apply{
		Command:            CommandPlan,
		Action:             ActionDeploy,
		RepoURL:            "r",
		Path:               "p",
		Backend:            backend,
		BackendConfig:      cfg,
		PlanArtifactSecret: "tfplan-run1-plan1",
	})
	if strings.Contains(s, "KUBE_CONFIG_PATH") {
		t.Errorf("no cluster auth must not export KUBE_CONFIG_PATH\n---\n%s", s)
	}
}

func TestScriptGolden(t *testing.T) {
	// Byte-for-byte guard: a representative Apply renders exactly this output —
	// the s3 backend override written before init and no injected KUBECONFIG.
	backend, cfg := s3Backend()
	s := Script(Apply{
		Command:       CommandPlan,
		Action:        ActionDeploy,
		RepoURL:       "https://github.com/acme/infra.git",
		Path:          "envs/prod",
		Backend:       backend,
		BackendConfig: cfg,
	})
	want := "#!/bin/sh\nset -e\n" +
		"git clone --depth 1 'https://github.com/acme/infra.git' /src\n" +
		"echo \"SPACEFLEET_CHART_REVISION=$(git -C /src rev-parse HEAD)\"\n" +
		"cd '/src/envs/prod'\n" +
		"cat > backend_override.tf <<'EOF'\n" +
		"terraform {\n" +
		"  backend \"s3\" {\n" +
		"    bucket = \"my-state\"\n" +
		"    key = \"prod/terraform.tfstate\"\n" +
		"    region = \"us-east-1\"\n" +
		"  }\n" +
		"}\n" +
		"EOF\n" +
		"tofu init -no-color\n" +
		"tofu plan -out=tfplan -no-color\n"
	if s != want {
		t.Errorf("rendered script changed\n got: %q\nwant: %q", s, want)
	}
}

func TestScriptInitAndPlanFlags(t *testing.T) {
	// init_flags append after -no-color on init; plan_flags append after the fixed
	// plan flags, each token shell-quoted as one argument.
	backend, cfg := s3Backend()
	s := Script(Apply{
		Command:       CommandPlan,
		Action:        ActionDeploy,
		RepoURL:       "r",
		Path:          "p",
		Backend:       backend,
		BackendConfig: cfg,
		InitFlags:     []string{"-upgrade"},
		PlanFlags:     []string{"-var=env=prod", "-target=aws_instance.web"},
	})
	if !strings.Contains(s, "tofu init -no-color '-upgrade'\n") {
		t.Errorf("init flags not appended\n---\n%s", s)
	}
	if !strings.Contains(s, "tofu plan -out=tfplan -no-color '-var=env=prod' '-target=aws_instance.web'\n") {
		t.Errorf("plan flags not appended in order\n---\n%s", s)
	}
}

func TestScriptApplyFlagsBeforePlanfile(t *testing.T) {
	// apply_flags go between -no-color and the positional planfile arg
	// (tofu apply [options] PLAN).
	backend, cfg := s3Backend()
	s := Script(Apply{
		Command:            CommandApply,
		Action:             ActionDeploy,
		RepoURL:            "r",
		Path:               "p",
		Backend:            backend,
		BackendConfig:      cfg,
		Namespace:          "default",
		PlanArtifactSecret: "tfplan-run1-plan1",
		ApplyFlags:         []string{"-parallelism=20"},
	})
	if !strings.Contains(s, "tofu apply -no-color '-parallelism=20' tfplan\n") {
		t.Errorf("apply flags must precede the planfile arg\n---\n%s", s)
	}
}

func TestScriptPlanFlagsAppliedToPreviewAndDestroy(t *testing.T) {
	// plan_flags also reach a preview plan and a destroy plan; apply_flags are inert
	// on a preview (no apply runs).
	preview := Script(Apply{
		Command:    CommandPlan,
		Action:     ActionPreview,
		RepoURL:    "r",
		Path:       "p",
		Backend:    "s3",
		PlanFlags:  []string{"-refresh=false"},
		ApplyFlags: []string{"-parallelism=20"},
	})
	if !strings.Contains(preview, "tofu plan -no-color '-refresh=false'\n") {
		t.Errorf("plan flags must reach a preview plan\n---\n%s", preview)
	}
	if strings.Contains(preview, "-parallelism=20") {
		t.Errorf("apply flags must not appear on a preview (no apply runs)\n---\n%s", preview)
	}
	destroy := Script(Apply{
		Command:   CommandPlan,
		Action:    ActionUninstall,
		RepoURL:   "r",
		Path:      "p",
		Backend:   "s3",
		PlanFlags: []string{"-refresh=false"},
	})
	if !strings.Contains(destroy, "tofu plan -destroy -out=tfplan -no-color '-refresh=false'\n") {
		t.Errorf("plan flags must reach a destroy plan\n---\n%s", destroy)
	}
}

func TestScriptFlagsShellQuotedAndBlanksSkipped(t *testing.T) {
	// A flag value with shell metacharacters is quoted as one whole token so it
	// can't break out of the command line; a blank token is dropped (no bare '').
	s := Script(Apply{
		Command:   CommandPlan,
		Action:    ActionDeploy,
		RepoURL:   "r",
		Path:      "p",
		Backend:   "s3",
		PlanFlags: []string{"-var=msg=a; rm -rf /", ""},
	})
	if !strings.Contains(s, "'-var=msg=a; rm -rf /'") {
		t.Errorf("flag with metacharacters must be shell-quoted as one token\n---\n%s", s)
	}
	if strings.Contains(s, "-no-color ''") {
		t.Errorf("blank flag token must be skipped\n---\n%s", s)
	}
}

func TestScriptNoFlagsUnchanged(t *testing.T) {
	// Empty flag slices render exactly as before (no trailing space, no quotes).
	s := Script(Apply{
		Command:            CommandApply,
		Action:             ActionDeploy,
		RepoURL:            "r",
		Path:               "p",
		Backend:            "s3",
		Namespace:          "default",
		PlanArtifactSecret: "tfplan-run1-plan1",
	})
	if !strings.Contains(s, "tofu init -no-color\n") {
		t.Errorf("no init flags must yield a plain init\n---\n%s", s)
	}
	if !strings.Contains(s, "tofu apply -no-color tfplan\n") {
		t.Errorf("no apply flags must yield a plain apply\n---\n%s", s)
	}
}

func TestScriptAllowsDottedNames(t *testing.T) {
	s := Script(Apply{Command: CommandPlan, Action: ActionDeploy, RepoURL: "r", Path: "envs/..foo/prod", Backend: "s3"})
	if strings.Contains(s, "path traversal not allowed") {
		t.Errorf("dotted name wrongly rejected\n---\n%s", s)
	}
	if !strings.Contains(s, "cd '/src/envs/..foo/prod'") {
		t.Errorf("expected cd into dotted path\n---\n%s", s)
	}
}
