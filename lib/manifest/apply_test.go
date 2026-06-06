package manifest

import (
	"strings"
	"testing"

	"github.com/spacefleet/spacefleet/lib/helm"
)

func TestScriptDeployCloneAndApply(t *testing.T) {
	s := Script(Apply{
		Action:  ActionDeploy,
		RepoURL: "https://github.com/acme/manifests.git",
		Path:    "k8s/prod",
	})
	wantContains := []string{
		"#!/bin/sh\nset -e\n",
		"git clone --depth 1 'https://github.com/acme/manifests.git' /src",
		"echo \"SPACEFLEET_CHART_REVISION=$(git -C /src rev-parse HEAD)\"",
		"kubectl apply -f '/src/k8s/prod' --kubeconfig '/workspace/creds/kubeconfig'",
	}
	for _, w := range wantContains {
		if !strings.Contains(s, w) {
			t.Errorf("script missing %q\n---\n%s", w, s)
		}
	}
	if strings.Contains(s, "kubectl delete") {
		t.Error("deploy should not run kubectl delete")
	}
	if strings.Contains(s, "--ignore-not-found") {
		t.Error("deploy should not use --ignore-not-found")
	}
	if strings.Contains(s, "--branch") {
		t.Error("no git_ref set, should not pass --branch")
	}
	if strings.Contains(s, "credential.helper") {
		t.Error("no token, should not wire a credential helper")
	}
}

func TestScriptPreviewCloneAndDiff(t *testing.T) {
	s := Script(Apply{
		Action:  ActionPreview,
		RepoURL: "https://github.com/acme/manifests.git",
		Path:    "k8s/prod",
	})
	wantContains := []string{
		// Still clones (a diff needs the manifests) and echoes the resolved SHA.
		"git clone --depth 1 'https://github.com/acme/manifests.git' /src",
		"echo \"SPACEFLEET_CHART_REVISION=$(git -C /src rev-parse HEAD)\"",
		// Sentinels bracket the diff body — the SAME strings lib/helm emits, so
		// helm.ParseDiff slices it out identically.
		"echo " + helm.DiffBeginMarker,
		"echo " + helm.DiffEndMarker,
		// A read-only kubectl diff against the live cluster (not apply/delete).
		"kubectl diff -f '/src/k8s/prod' --kubeconfig '/workspace/creds/kubeconfig'",
		// Exit code captured without failing the step; the verdict rides out in the
		// changes marker; >1 is a real error.
		"set +e",
		"diff_rc=$?",
		"set -e",
		"if [ \"$diff_rc\" -gt 1 ]; then echo 'kubectl diff failed' >&2; exit 1; fi",
		helm.DiffChangesPrefix + "true",
		helm.DiffChangesPrefix + "false",
		"exit 0",
	}
	for _, w := range wantContains {
		if !strings.Contains(s, w) {
			t.Errorf("preview script missing %q\n---\n%s", w, s)
		}
	}
	// A preview must change nothing on the cluster.
	if strings.Contains(s, "kubectl apply") {
		t.Error("preview must not run kubectl apply")
	}
	if strings.Contains(s, "kubectl delete") {
		t.Error("preview must not run kubectl delete")
	}

	// The emitted markers must be exactly what helm.ParseDiff reads back: simulate a
	// captured run where the diff body sits between the sentinels and changes=true.
	logs := strings.Join([]string{
		"cloning...",
		helm.DiffBeginMarker,
		"--- a",
		"+++ b",
		helm.DiffEndMarker,
		helm.DiffChangesPrefix + "true",
	}, "\n")
	d := helm.ParseDiff(logs)
	if !d.HasChanges {
		t.Error("ParseDiff should report HasChanges=true for the manifest preview markers")
	}
	if d.Body != "--- a\n+++ b" {
		t.Errorf("ParseDiff body = %q, want the lines between the sentinels", d.Body)
	}
}

func TestScriptUninstallCloneAndDelete(t *testing.T) {
	s := Script(Apply{
		Action:  ActionUninstall,
		RepoURL: "https://github.com/acme/manifests.git",
		Path:    "k8s/prod/app.yaml",
	})
	wantContains := []string{
		"git clone --depth 1 'https://github.com/acme/manifests.git' /src",
		"kubectl delete -f '/src/k8s/prod/app.yaml' --ignore-not-found --kubeconfig '/workspace/creds/kubeconfig'",
	}
	for _, w := range wantContains {
		if !strings.Contains(s, w) {
			t.Errorf("script missing %q\n---\n%s", w, s)
		}
	}
	if strings.Contains(s, "kubectl apply") {
		t.Error("uninstall should not run kubectl apply")
	}
}

func TestScriptGitRefBranchFlag(t *testing.T) {
	withRef := Script(Apply{Action: ActionDeploy, RepoURL: "r", GitRef: "release-1", Path: "p"})
	if !strings.Contains(withRef, "git clone --depth 1 --branch 'release-1' 'r' /src") {
		t.Errorf("expected --branch with git_ref\n---\n%s", withRef)
	}
	noRef := Script(Apply{Action: ActionDeploy, RepoURL: "r", Path: "p"})
	if strings.Contains(noRef, "--branch") {
		t.Errorf("expected no --branch without git_ref\n---\n%s", noRef)
	}
}

func TestScriptTokenWiresCredentialHelper(t *testing.T) {
	withTok := Script(Apply{Action: ActionDeploy, RepoURL: "r", Path: "p", HasGitToken: true})
	if !strings.Contains(withTok, "git config --global credential.helper 'store --file=/workspace/creds/git-credentials'") {
		t.Errorf("expected credential helper wired when HasGitToken\n---\n%s", withTok)
	}
	// The token must never appear in the script string itself.
	if strings.Contains(withTok, "x-access-token") || strings.Contains(withTok, "@github.com") {
		t.Errorf("token material must not appear in the script\n---\n%s", withTok)
	}
	withoutTok := Script(Apply{Action: ActionDeploy, RepoURL: "r", Path: "p"})
	if strings.Contains(withoutTok, "credential.helper") {
		t.Errorf("no credential helper expected without a token\n---\n%s", withoutTok)
	}
}

func TestScriptTokenWiredForUninstall(t *testing.T) {
	s := Script(Apply{Action: ActionUninstall, RepoURL: "r", Path: "p", HasGitToken: true})
	if !strings.Contains(s, "credential.helper 'store --file=/workspace/creds/git-credentials'") {
		t.Errorf("uninstall must still wire the credential helper to clone\n---\n%s", s)
	}
}

func TestScriptRejectsPathTraversal(t *testing.T) {
	for _, p := range []string{"../etc", "k8s/../../secret", ".."} {
		s := Script(Apply{Action: ActionDeploy, RepoURL: "r", Path: p})
		if !strings.Contains(s, "path traversal not allowed") {
			t.Errorf("path %q should be rejected\n---\n%s", p, s)
		}
		if strings.Contains(s, "kubectl apply") {
			t.Errorf("path %q rejected but apply still emitted\n---\n%s", p, s)
		}
		if strings.Contains(s, "git clone") {
			t.Errorf("path %q rejected but clone still emitted\n---\n%s", p, s)
		}
	}
}

func TestScriptAllowsDottedNames(t *testing.T) {
	// Legitimate names containing dots (but not a bare ".." segment) are allowed.
	s := Script(Apply{Action: ActionDeploy, RepoURL: "r", Path: "deploy/..foo/app.v2.yaml"})
	if strings.Contains(s, "path traversal not allowed") {
		t.Errorf("dotted name wrongly rejected\n---\n%s", s)
	}
	if !strings.Contains(s, "kubectl apply -f '/src/deploy/..foo/app.v2.yaml'") {
		t.Errorf("expected apply of dotted path\n---\n%s", s)
	}
}
