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

func TestScriptHTTPRepoWithCredential(t *testing.T) {
	s := Script(Rollout{
		Action:          ActionDeploy,
		ChartSource:     SourceHTTPRepo,
		Config:          map[string]string{ConfigRepoURL: "https://charts.example.com", ConfigChart: "nginx"},
		ReleaseName:     "web",
		TargetNamespace: "apps",
		WaitTimeout:     30 * time.Minute,
		HasCredential:   true,
	})
	// Auth is read from the mounted files on the repo add, never the password
	// itself in the script.
	want := "helm repo add r 'https://charts.example.com' --username \"$(cat '/workspace/creds/registry-username')\" --password \"$(cat '/workspace/creds/registry-password')\""
	if !strings.Contains(s, want) {
		t.Errorf("script missing credentialed repo add\nwant: %s\n---\n%s", want, s)
	}
}

func TestScriptOCIWithCredential(t *testing.T) {
	s := Script(Rollout{
		Action:          ActionUpgrade,
		ChartSource:     SourceOCI,
		Config:          map[string]string{ConfigRepoURL: "oci://reg.example.com/charts/app"},
		ReleaseName:     "app",
		TargetNamespace: "prod",
		WaitTimeout:     10 * time.Minute,
		HasCredential:   true,
	})
	// registry login uses --password-stdin (the password file redirected as
	// stdin), so the password never reaches the process args, and the host is the
	// registry host only — not the full chart path.
	want := "helm registry login 'reg.example.com' --username \"$(cat '/workspace/creds/registry-username')\" --password-stdin < '/workspace/creds/registry-password'"
	if !strings.Contains(s, want) {
		t.Errorf("script missing registry login\nwant: %s\n---\n%s", want, s)
	}
	// The login must come before the pull.
	if strings.Index(s, "registry login") > strings.Index(s, "upgrade --install") {
		t.Errorf("registry login must precede the chart pull:\n%s", s)
	}
}

func TestScriptUninstallIgnoresCredential(t *testing.T) {
	s := Script(Rollout{
		Action:          ActionUninstall,
		ChartSource:     SourceOCI,
		Config:          map[string]string{ConfigRepoURL: "oci://reg.example.com/charts/app"},
		ReleaseName:     "app",
		TargetNamespace: "prod",
		WaitTimeout:     10 * time.Minute,
		HasCredential:   true,
	})
	// Uninstall pulls no chart, so it needs no registry login.
	if strings.Contains(s, "registry login") {
		t.Errorf("uninstall should not log in to a registry:\n%s", s)
	}
}

func TestOCIRegistryHost(t *testing.T) {
	cases := map[string]string{
		"oci://reg.example.com/charts/app": "reg.example.com",
		"oci://reg.example.com":            "reg.example.com",
		"reg.example.com/charts/app":       "reg.example.com",
	}
	for in, want := range cases {
		if got := ociRegistryHost(in); got != want {
			t.Errorf("ociRegistryHost(%q) = %q, want %q", in, got, want)
		}
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

func TestScriptGitWithToken(t *testing.T) {
	s := Script(Rollout{
		Action:      ActionDeploy,
		ChartSource: SourceGit,
		Config: map[string]string{
			ConfigRepoURL: "https://github.com/org/charts.git",
			ConfigGitPath: "charts/app",
		},
		ReleaseName:     "app",
		TargetNamespace: "ns",
		WaitTimeout:     30 * time.Minute,
		HasGitToken:     true,
	})
	// The token is read from the mounted file via git's credential helper; it
	// never appears in the script string or the clone's argv.
	want := "git config --global credential.helper 'store --file=/workspace/creds/git-credentials'"
	if !strings.Contains(s, want) {
		t.Errorf("git script missing credential helper\nwant: %s\n---\n%s", want, s)
	}
	// The helper must be configured before the clone so the clone can use it.
	if strings.Index(s, "credential.helper") > strings.Index(s, "git clone") {
		t.Errorf("credential helper must precede the clone:\n%s", s)
	}
	if strings.Contains(s, "x-access-token") {
		t.Errorf("token material must not appear in the script string:\n%s", s)
	}
}

func TestScriptGitNoTokenOmitsHelper(t *testing.T) {
	s := Script(Rollout{
		Action:          ActionDeploy,
		ChartSource:     SourceGit,
		Config:          map[string]string{ConfigRepoURL: "https://github.com/org/charts.git"},
		ReleaseName:     "app",
		TargetNamespace: "ns",
		WaitTimeout:     30 * time.Minute,
	})
	if strings.Contains(s, "credential.helper") {
		t.Errorf("public git clone should not configure a credential helper:\n%s", s)
	}
}

// valuesSource builds one values-source map for a test.
func valuesSource(repo, ref, path string) map[string]string {
	src := map[string]string{ValuesSourceRepoURL: repo, ValuesSourcePath: path}
	if ref != "" {
		src[ValuesSourceGitRef] = ref
	}
	return src
}

func TestScriptValuesFromGit(t *testing.T) {
	s := Script(Rollout{
		Action:      ActionDeploy,
		ChartSource: SourceHTTPRepo,
		Config: map[string]string{
			ConfigRepoURL: "https://charts.example.com",
			ConfigChart:   "nginx",
		},
		ValuesSources: []map[string]string{
			valuesSource("https://github.com/org/config.git", "main", "envs/prod.yaml"),
		},
		ReleaseName:     "web",
		TargetNamespace: "apps",
		WaitTimeout:     30 * time.Minute,
	})
	// The single source is cloned to /values/0…
	if !strings.Contains(s, "git clone --depth 1 --branch 'main' 'https://github.com/org/config.git' '/values/0'") {
		t.Errorf("script missing values clone:\n%s", s)
	}
	// …and layered under the inline values: the git file's -f must come before the
	// inline values.yaml -f so the inline override wins (helm: last -f wins).
	gitFlag := "-f '/values/0/envs/prod.yaml'"
	inlineFlag := "-f '/workspace/creds/values.yaml'"
	gi, ii := strings.Index(s, gitFlag), strings.Index(s, inlineFlag)
	if gi < 0 || ii < 0 {
		t.Fatalf("script missing a values -f flag (git=%d inline=%d):\n%s", gi, ii, s)
	}
	if gi > ii {
		t.Errorf("git values -f must precede inline values -f so inline overrides:\n%s", s)
	}
}

func TestScriptValuesFromGitMultiple(t *testing.T) {
	// Two sources layer in order: base first, then override, then inline values.
	s := Script(Rollout{
		Action:      ActionDeploy,
		ChartSource: SourceOCI,
		Config:      map[string]string{ConfigRepoURL: "oci://reg.example.com/charts/app"},
		ValuesSources: []map[string]string{
			valuesSource("https://github.com/org/base.git", "", "base.yaml"),
			valuesSource("https://github.com/org/over.git", "release-1", "prod.yaml"),
		},
		ReleaseName:     "app",
		TargetNamespace: "prod",
		WaitTimeout:     10 * time.Minute,
	})
	// Each source clones to its own indexed dir.
	for _, w := range []string{
		"git clone --depth 1 'https://github.com/org/base.git' '/values/0'",
		"git clone --depth 1 --branch 'release-1' 'https://github.com/org/over.git' '/values/1'",
	} {
		if !strings.Contains(s, w) {
			t.Errorf("script missing clone %q\n---\n%s", w, s)
		}
	}
	// -f flags appear in source order, then the inline values last.
	order := []string{
		"-f '/values/0/base.yaml'",
		"-f '/values/1/prod.yaml'",
		"-f '/workspace/creds/values.yaml'",
	}
	prev := -1
	for _, f := range order {
		at := strings.Index(s, f)
		if at < 0 {
			t.Fatalf("script missing -f %q:\n%s", f, s)
		}
		if at < prev {
			t.Errorf("values -f flags out of order at %q:\n%s", f, s)
		}
		prev = at
	}
}

func TestScriptValuesFromGitWithToken(t *testing.T) {
	// An OCI chart (not a git chart) with values pulled from a private GitHub repo:
	// the credential helper is wired even though the chart source isn't git.
	s := Script(Rollout{
		Action:      ActionDeploy,
		ChartSource: SourceOCI,
		Config:      map[string]string{ConfigRepoURL: "oci://reg.example.com/charts/app"},
		ValuesSources: []map[string]string{
			valuesSource("https://github.com/org/config.git", "", "values.yaml"),
		},
		ReleaseName:     "app",
		TargetNamespace: "prod",
		WaitTimeout:     10 * time.Minute,
		HasGitToken:     true,
	})
	helper := "git config --global credential.helper 'store --file=/workspace/creds/git-credentials'"
	if !strings.Contains(s, helper) {
		t.Errorf("script missing credential helper for git-sourced values:\n%s", s)
	}
	// The helper must precede the values clone so the clone can use it.
	if strings.Index(s, "credential.helper") > strings.Index(s, "git clone") {
		t.Errorf("credential helper must precede the values clone:\n%s", s)
	}
	if strings.Contains(s, "x-access-token") {
		t.Errorf("token material must not appear in the script string:\n%s", s)
	}
}

func TestScriptUninstallSkipsValuesClone(t *testing.T) {
	s := Script(Rollout{
		Action:      ActionUninstall,
		ChartSource: SourceHTTPRepo,
		ValuesSources: []map[string]string{
			valuesSource("https://github.com/org/config.git", "", "values.yaml"),
		},
		ReleaseName:     "web",
		TargetNamespace: "apps",
		WaitTimeout:     30 * time.Minute,
	})
	// Uninstall pulls no chart and no values, so it must not clone anything.
	if strings.Contains(s, "git clone") {
		t.Errorf("uninstall should not clone a values repo:\n%s", s)
	}
}

func TestScriptEchoesRevisions(t *testing.T) {
	// A git chart with two git-sourced values echoes the chart SHA and a per-source
	// indexed values SHA, so the worker can record what the run pulled.
	s := Script(Rollout{
		Action:      ActionDeploy,
		ChartSource: SourceGit,
		Config:      map[string]string{ConfigRepoURL: "https://github.com/org/charts.git"},
		ValuesSources: []map[string]string{
			valuesSource("https://github.com/org/base.git", "", "base.yaml"),
			valuesSource("https://github.com/org/over.git", "", "prod.yaml"),
		},
		ReleaseName:     "app",
		TargetNamespace: "ns",
		WaitTimeout:     30 * time.Minute,
	})
	for _, w := range []string{
		`echo "SPACEFLEET_CHART_REVISION=$(git -C /src rev-parse HEAD)"`,
		`echo "SPACEFLEET_VALUES_REVISION_0=$(git -C '/values/0' rev-parse HEAD)"`,
		`echo "SPACEFLEET_VALUES_REVISION_1=$(git -C '/values/1' rev-parse HEAD)"`,
	} {
		if !strings.Contains(s, w) {
			t.Errorf("script missing revision echo %q\n---\n%s", w, s)
		}
	}

	// An http_repo chart with no values-from-git echoes neither (nothing cloned).
	s2 := Script(Rollout{
		Action:          ActionDeploy,
		ChartSource:     SourceHTTPRepo,
		Config:          map[string]string{ConfigRepoURL: "https://c", ConfigChart: "x"},
		ReleaseName:     "r",
		TargetNamespace: "n",
		WaitTimeout:     time.Minute,
	})
	if strings.Contains(s2, "REVISION") {
		t.Errorf("non-git rollout should echo no revision:\n%s", s2)
	}
}

func TestParseRevisions(t *testing.T) {
	logs := strings.Join([]string{
		"Cloning into '/src'...",
		"SPACEFLEET_CHART_REVISION=abc123",
		"Cloning into '/values/0'...",
		"SPACEFLEET_VALUES_REVISION_0=def456",
		"Cloning into '/values/1'...",
		"SPACEFLEET_VALUES_REVISION_1=789abc",
		"Release \"app\" has been upgraded.",
	}, "\n")
	rev := ParseRevisions(logs)
	if rev.Chart != "abc123" {
		t.Errorf("Chart = %q, want abc123", rev.Chart)
	}
	if rev.Values[0] != "def456" || rev.Values[1] != "789abc" {
		t.Errorf("Values = %v, want {0:def456 1:789abc}", rev.Values)
	}

	// Last occurrence of a marker wins (a retried clone within one captured log).
	rev = ParseRevisions("SPACEFLEET_VALUES_REVISION_0=old\nSPACEFLEET_VALUES_REVISION_0=new\n")
	if rev.Values[0] != "new" {
		t.Errorf("Values[0] = %q, want new (last wins)", rev.Values[0])
	}

	// No markers → empty maps/strings, never a panic.
	if rev := ParseRevisions("just helm output\n"); rev.Chart != "" || len(rev.Values) != 0 {
		t.Errorf("expected empty revisions, got %+v", rev)
	}
}

func TestScriptPreview(t *testing.T) {
	s := Script(Rollout{
		Action:      ActionPreview,
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
		// The helm-diff plugin is installed (idempotently) before the diff.
		"helm plugin install https://github.com/databus23/helm-diff --version '" + helmDiffVersion + "'",
		// The diff runs against the live cluster and reports changes via exit code.
		"helm diff upgrade 'web' 'r/nginx' --install --three-way-merge --detailed-exitcode --no-color -C 5 --version '1.2.3'",
		"-n 'apps'",
		"-f '/workspace/creds/values.yaml'",
		"--kubeconfig '/workspace/creds/kubeconfig'",
		// Sentinels bracket the diff body; the verdict rides out in a marker.
		"echo " + DiffBeginMarker,
		"echo " + DiffEndMarker,
		DiffChangesPrefix + "true",
		DiffChangesPrefix + "false",
		// Exit 0 on a successful diff (changes are data, not failure); exit 1 only on
		// a real diff failure.
		"exit 0",
	}
	for _, w := range wantContains {
		if !strings.Contains(s, w) {
			t.Errorf("preview script missing %q\n---\n%s", w, s)
		}
	}
	// A preview must not mutate the cluster: no install, no namespace creation, no
	// --wait.
	for _, banned := range []string{"helm upgrade --install", "--create-namespace", "--wait"} {
		if strings.Contains(s, banned) {
			t.Errorf("preview script must not contain %q\n---\n%s", banned, s)
		}
	}
	// The plugin install must precede the diff.
	if strings.Index(s, "plugin install") > strings.Index(s, "helm diff") {
		t.Errorf("plugin install must precede the diff:\n%s", s)
	}
}

func TestScriptPreviewGit(t *testing.T) {
	// A git-chart preview still echoes the resolved chart SHA (the same setup as a
	// rollout) so a refresh records the desired revision a deploy would pull.
	s := Script(Rollout{
		Action:          ActionPreview,
		ChartSource:     SourceGit,
		Config:          map[string]string{ConfigRepoURL: "https://github.com/org/charts.git", ConfigGitPath: "charts/app"},
		ReleaseName:     "app",
		TargetNamespace: "ns",
		WaitTimeout:     30 * time.Minute,
	})
	for _, w := range []string{
		"git clone --depth 1 'https://github.com/org/charts.git' /src",
		"helm dependency build",
		`echo "SPACEFLEET_CHART_REVISION=$(git -C /src rev-parse HEAD)"`,
		"helm diff upgrade 'app' '.' --install",
	} {
		if !strings.Contains(s, w) {
			t.Errorf("git preview script missing %q\n---\n%s", w, s)
		}
	}
}

func TestParseDiff(t *testing.T) {
	logs := strings.Join([]string{
		"installing helm-diff...",
		"SPACEFLEET_DIFF_BEGIN",
		"apps, web, Deployment (apps) has changed:",
		"- replicas: 2",
		"+ replicas: 3",
		"SPACEFLEET_DIFF_END",
		"SPACEFLEET_DIFF_CHANGES=true",
	}, "\n")
	d := ParseDiff(logs)
	if !d.HasChanges {
		t.Error("expected HasChanges=true")
	}
	wantBody := "apps, web, Deployment (apps) has changed:\n- replicas: 2\n+ replicas: 3"
	if d.Body != wantBody {
		t.Errorf("Body = %q, want %q", d.Body, wantBody)
	}
	// The setup chatter before BEGIN and the marker after END are excluded.
	if strings.Contains(d.Body, "installing helm-diff") || strings.Contains(d.Body, "SPACEFLEET_DIFF_CHANGES") {
		t.Errorf("body should exclude setup chatter and markers: %q", d.Body)
	}

	// No changes: empty diff body, HasChanges=false.
	none := ParseDiff("SPACEFLEET_DIFF_BEGIN\nSPACEFLEET_DIFF_END\nSPACEFLEET_DIFF_CHANGES=false\n")
	if none.HasChanges || none.Body != "" {
		t.Errorf("expected no changes and empty body, got %+v", none)
	}

	// Missing markers (e.g. a run that failed before the diff) → empty, no panic.
	if d := ParseDiff("helm diff failed\n"); d.HasChanges || d.Body != "" {
		t.Errorf("expected empty diff for marker-less logs, got %+v", d)
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

func TestScriptForce(t *testing.T) {
	s := Script(Rollout{
		Action:      ActionDeploy,
		ChartSource: SourceHTTPRepo,
		Config:      map[string]string{ConfigRepoURL: "https://c", ConfigChart: "nginx"},
		ReleaseName: "web",
		// A namespace with a shell metacharacter shouldn't be possible, but confirm
		// the selector/namespace are quoted, not interpolated raw.
		TargetNamespace: "apps",
		WaitTimeout:     10 * time.Minute,
		Force:           true,
	})
	for _, w := range []string{
		// The workloads are resolved by the standard instance label…
		"kubectl get deployment,statefulset,daemonset -n 'apps' -l 'app.kubernetes.io/instance=web' -o name --kubeconfig '/workspace/creds/kubeconfig'",
		// …each is restarted…
		`kubectl rollout restart "$t" -n 'apps' --kubeconfig '/workspace/creds/kubeconfig'`,
		// …and waited on under the same timeout the upgrade used.
		`kubectl rollout status "$t" -n 'apps' --timeout '10m0s' --kubeconfig '/workspace/creds/kubeconfig'`,
	} {
		if !strings.Contains(s, w) {
			t.Errorf("force script missing %q\n---\n%s", w, s)
		}
	}
	// The restart must come after the upgrade, never before.
	if strings.Index(s, "rollout restart") < strings.Index(s, "helm upgrade --install") {
		t.Errorf("force restart must follow the helm upgrade:\n%s", s)
	}
}

func TestScriptForceOmittedWhenUnset(t *testing.T) {
	// A normal (non-forced) deploy must not restart anything.
	s := Script(Rollout{
		Action:          ActionDeploy,
		ChartSource:     SourceHTTPRepo,
		Config:          map[string]string{ConfigRepoURL: "https://c", ConfigChart: "x"},
		ReleaseName:     "web",
		TargetNamespace: "apps",
		WaitTimeout:     time.Minute,
	})
	if strings.Contains(s, "rollout restart") {
		t.Errorf("non-forced deploy should not roll workloads:\n%s", s)
	}
}

func TestScriptForceIgnoredByPreview(t *testing.T) {
	// A preview must never mutate the cluster, even with Force set.
	s := Script(Rollout{
		Action:          ActionPreview,
		ChartSource:     SourceHTTPRepo,
		Config:          map[string]string{ConfigRepoURL: "https://c", ConfigChart: "x"},
		ReleaseName:     "web",
		TargetNamespace: "apps",
		WaitTimeout:     time.Minute,
		Force:           true,
	})
	if strings.Contains(s, "rollout restart") {
		t.Errorf("preview must not roll workloads even when forced:\n%s", s)
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
