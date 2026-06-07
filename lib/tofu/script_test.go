package tofu

import (
	"strings"
	"testing"
)

func TestScriptPlanDeployKubernetesBackend(t *testing.T) {
	s := Script(Apply{
		Command:      CommandPlan,
		Action:       ActionDeploy,
		RepoURL:      "https://github.com/acme/infra.git",
		Path:         "envs/prod",
		SecretSuffix: "app1-comp1",
		Namespace:    "default",
	})
	wantContains := []string{
		"#!/bin/sh\nset -e\n",
		"git clone --depth 1 'https://github.com/acme/infra.git' /src",
		"echo \"SPACEFLEET_CHART_REVISION=$(git -C /src rev-parse HEAD)\"",
		"cd '/src/envs/prod'",
		"export KUBECONFIG='/workspace/creds/kubeconfig'",
		"cat > backend_override.tf <<'EOF'",
		"backend \"kubernetes\" {",
		"secret_suffix    = \"app1-comp1\"",
		"namespace        = \"default\"",
		"in_cluster_config = false",
		"tofu init -no-color",
		"tofu plan -no-color",
	}
	for _, w := range wantContains {
		if !strings.Contains(s, w) {
			t.Errorf("script missing %q\n---\n%s", w, s)
		}
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
}

func TestScriptPlanUninstallDestroy(t *testing.T) {
	s := Script(Apply{
		Command:      CommandPlan,
		Action:       ActionUninstall,
		RepoURL:      "r",
		Path:         "p",
		SecretSuffix: "s",
	})
	if !strings.Contains(s, "tofu plan -destroy -no-color") {
		t.Errorf("uninstall plan must be a destroy plan\n---\n%s", s)
	}
	if strings.Contains(s, "tofu apply") || strings.Contains(s, "tofu destroy") {
		t.Errorf("plan node must not mutate\n---\n%s", s)
	}
}

func TestScriptApplyDeploy(t *testing.T) {
	s := Script(Apply{
		Command:      CommandApply,
		Action:       ActionDeploy,
		RepoURL:      "r",
		Path:         "p",
		SecretSuffix: "s",
	})
	if !strings.Contains(s, "tofu apply -auto-approve -no-color") {
		t.Errorf("apply/deploy must auto-approve apply\n---\n%s", s)
	}
	if strings.Contains(s, "tofu destroy") {
		t.Error("deploy apply must not destroy")
	}
	if strings.Contains(s, "tofu plan") {
		t.Error("apply node should not run a standalone plan (apply re-plans implicitly)")
	}
}

func TestScriptApplyUninstallDestroy(t *testing.T) {
	s := Script(Apply{
		Command:      CommandApply,
		Action:       ActionUninstall,
		RepoURL:      "r",
		Path:         "p",
		SecretSuffix: "s",
	})
	if !strings.Contains(s, "tofu destroy -auto-approve -no-color") {
		t.Errorf("apply/uninstall must auto-approve destroy\n---\n%s", s)
	}
	if strings.Contains(s, "tofu apply") {
		t.Error("uninstall must not apply")
	}
}

func TestScriptPreviewIsAlwaysPlan(t *testing.T) {
	// An apply node previewed must still be a read-only plan (preview never mutates).
	s := Script(Apply{
		Command:      CommandApply,
		Action:       ActionPreview,
		RepoURL:      "r",
		Path:         "p",
		SecretSuffix: "s",
	})
	if !strings.Contains(s, "tofu plan -no-color") {
		t.Errorf("preview must be a read-only plan\n---\n%s", s)
	}
	if strings.Contains(s, "tofu apply") || strings.Contains(s, "tofu destroy") {
		t.Errorf("preview must not mutate\n---\n%s", s)
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
	})
	wantContains := []string{
		"backend \"s3\" {",
		// Rendered in sorted key order for stable output.
		"bucket = \"my-state\"",
		"key = \"prod/terraform.tfstate\"",
		"region = \"us-east-1\"",
		"tofu apply -auto-approve -no-color",
	}
	for _, w := range wantContains {
		if !strings.Contains(s, w) {
			t.Errorf("custom-backend script missing %q\n---\n%s", w, s)
		}
	}
	// Custom backend must not emit the kubernetes-default settings.
	if strings.Contains(s, "secret_suffix") || strings.Contains(s, "in_cluster_config") {
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
	withRef := Script(Apply{Command: CommandPlan, Action: ActionDeploy, RepoURL: "r", GitRef: "release-1", Path: "p", SecretSuffix: "s"})
	if !strings.Contains(withRef, "git clone --depth 1 --branch 'release-1' 'r' /src") {
		t.Errorf("expected --branch with git_ref\n---\n%s", withRef)
	}
	noRef := Script(Apply{Command: CommandPlan, Action: ActionDeploy, RepoURL: "r", Path: "p", SecretSuffix: "s"})
	if strings.Contains(noRef, "--branch") {
		t.Errorf("expected no --branch without git_ref\n---\n%s", noRef)
	}
}

func TestScriptTokenWiresCredentialHelper(t *testing.T) {
	withTok := Script(Apply{Command: CommandPlan, Action: ActionDeploy, RepoURL: "r", Path: "p", SecretSuffix: "s", HasGitToken: true})
	if !strings.Contains(withTok, "git config --global credential.helper 'store --file=/workspace/creds/git-credentials'") {
		t.Errorf("expected credential helper wired when HasGitToken\n---\n%s", withTok)
	}
	// The token must never appear in the script string itself.
	if strings.Contains(withTok, "x-access-token") || strings.Contains(withTok, "@github.com") {
		t.Errorf("token material must not appear in the script\n---\n%s", withTok)
	}
	withoutTok := Script(Apply{Command: CommandPlan, Action: ActionDeploy, RepoURL: "r", Path: "p", SecretSuffix: "s"})
	if strings.Contains(withoutTok, "credential.helper") {
		t.Errorf("no credential helper expected without a token\n---\n%s", withoutTok)
	}
}

func TestScriptTokenWiredForUninstall(t *testing.T) {
	// An uninstall (tofu destroy) still clones the root module, so the token wiring
	// is honored regardless of action.
	s := Script(Apply{Command: CommandApply, Action: ActionUninstall, RepoURL: "r", Path: "p", SecretSuffix: "s", HasGitToken: true})
	if !strings.Contains(s, "credential.helper 'store --file=/workspace/creds/git-credentials'") {
		t.Errorf("uninstall must still wire the credential helper to clone\n---\n%s", s)
	}
}

func TestScriptDefaultBackendWhenEmpty(t *testing.T) {
	// No Backend set falls back to the kubernetes default.
	s := Script(Apply{Command: CommandPlan, Action: ActionDeploy, RepoURL: "r", Path: "p", SecretSuffix: "s"})
	if !strings.Contains(s, "backend \"kubernetes\" {") {
		t.Errorf("empty backend should default to kubernetes\n---\n%s", s)
	}
}

func TestScriptRejectsPathTraversal(t *testing.T) {
	for _, p := range []string{"../etc", "envs/../../secret", ".."} {
		s := Script(Apply{Command: CommandPlan, Action: ActionDeploy, RepoURL: "r", Path: p, SecretSuffix: "s"})
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

func TestScriptAllowsDottedNames(t *testing.T) {
	s := Script(Apply{Command: CommandPlan, Action: ActionDeploy, RepoURL: "r", Path: "envs/..foo/prod", SecretSuffix: "s"})
	if strings.Contains(s, "path traversal not allowed") {
		t.Errorf("dotted name wrongly rejected\n---\n%s", s)
	}
	if !strings.Contains(s, "cd '/src/envs/..foo/prod'") {
		t.Errorf("expected cd into dotted path\n---\n%s", s)
	}
}
