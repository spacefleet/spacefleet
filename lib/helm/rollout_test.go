package helm

import (
	"strings"
	"testing"
	"time"

	"github.com/spacefleet/spacefleet/lib/k8s"
)

func TestScriptHTTPRepo(t *testing.T) {
	s := Script(Rollout{
		Action:      ActionDeploy,
		ChartSource: SourceHTTPRepo,
		Config: map[string]string{
			ConfigRepoURL: "https://charts.example.com",
			ConfigChart:   "nginx",
			ConfigVersion: "1.2.3",
		},
		ReleaseName:     "web",
		TargetNamespace: "apps",
		WaitTimeout:     30 * time.Minute,
	})
	wantContains := []string{
		"helm repo add r 'https://charts.example.com'",
		"helm repo update r",
		"helm upgrade --install 'web' 'r/nginx' --version '1.2.3'",
		"-n 'apps' --create-namespace --wait --timeout '30m0s'",
		"-f '/workspace/creds/values.yaml'",
		"--kubeconfig '/workspace/creds/kubeconfig'",
	}
	for _, w := range wantContains {
		if !strings.Contains(s, w) {
			t.Errorf("script missing %q\n---\n%s", w, s)
		}
	}
	if strings.Contains(s, "--ignore-not-found") {
		t.Error("deploy should not use --ignore-not-found")
	}
}

func TestScriptHTTPRepoNoVersion(t *testing.T) {
	s := Script(Rollout{
		Action:          ActionDeploy,
		ChartSource:     SourceHTTPRepo,
		Config:          map[string]string{ConfigRepoURL: "https://c", ConfigChart: "x"},
		ReleaseName:     "r",
		TargetNamespace: "ns",
		WaitTimeout:     10 * time.Minute,
	})
	if strings.Contains(s, "--version") {
		t.Errorf("no version supplied, should omit --version:\n%s", s)
	}
}

func TestScriptOCI(t *testing.T) {
	s := Script(Rollout{
		Action:          ActionUpgrade,
		ChartSource:     SourceOCI,
		Config:          map[string]string{ConfigRepoURL: "oci://reg.example.com/charts/app", ConfigVersion: "2.0.0"},
		ReleaseName:     "app",
		TargetNamespace: "prod",
		WaitTimeout:     10 * time.Minute,
	})
	if !strings.Contains(s, "helm upgrade --install 'app' 'oci://reg.example.com/charts/app' --version '2.0.0'") {
		t.Errorf("unexpected OCI script:\n%s", s)
	}
	if strings.Contains(s, "helm repo add") {
		t.Error("OCI should not add a repo")
	}
}

func TestScriptGit(t *testing.T) {
	s := Script(Rollout{
		Action:      ActionDeploy,
		ChartSource: SourceGit,
		Config: map[string]string{
			ConfigRepoURL: "https://git.example.com/r.git",
			ConfigGitRef:  "main",
			ConfigGitPath: "charts/app",
		},
		ReleaseName:     "app",
		TargetNamespace: "ns",
		WaitTimeout:     30 * time.Minute,
	})
	for _, w := range []string{
		"git clone --depth 1 --branch 'main' 'https://git.example.com/r.git' /src",
		"cd '/src/charts/app'",
		"helm dependency build",
		"helm upgrade --install 'app' '.'",
	} {
		if !strings.Contains(s, w) {
			t.Errorf("git script missing %q\n---\n%s", w, s)
		}
	}
}

func TestScriptUninstall(t *testing.T) {
	s := Script(Rollout{
		Action:          ActionUninstall,
		ReleaseName:     "web",
		TargetNamespace: "apps",
		WaitTimeout:     30 * time.Minute,
	})
	want := "helm uninstall 'web' -n 'apps' --wait --timeout '30m0s' --ignore-not-found --kubeconfig '/workspace/creds/kubeconfig'"
	if !strings.Contains(s, want) {
		t.Errorf("uninstall script missing %q\n---\n%s", want, s)
	}
}

func TestScriptAlwaysWaits(t *testing.T) {
	for _, src := range []string{SourceHTTPRepo, SourceOCI, SourceGit} {
		s := Script(Rollout{Action: ActionDeploy, ChartSource: src, Config: map[string]string{}, ReleaseName: "r", TargetNamespace: "n", WaitTimeout: time.Minute})
		if !strings.Contains(s, "--wait") {
			t.Errorf("source %s script missing --wait:\n%s", src, s)
		}
	}
}

func TestWaitTimeoutTiers(t *testing.T) {
	cases := map[k8s.Method]time.Duration{
		k8s.MethodEKS:        10 * time.Minute,
		k8s.MethodGKE:        10 * time.Minute,
		k8s.MethodAKS:        10 * time.Minute,
		k8s.MethodToken:      30 * time.Minute,
		k8s.MethodKubeconfig: 30 * time.Minute,
		k8s.MethodInCluster:  30 * time.Minute,
	}
	for m, want := range cases {
		if got := WaitTimeout(m); got != want {
			t.Errorf("WaitTimeout(%s) = %s, want %s", m, got, want)
		}
	}
}
