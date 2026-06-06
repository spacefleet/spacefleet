// Package helm drives Helm-release rollouts. A rollout runs as a single-step
// Tekton TaskRun on a chosen runner cluster (via lib/tekton): the step is a
// helm CLI container whose script targets the destination cluster through an
// injected kubeconfig. Go never runs helm itself — it only emits the shell
// script and hands lib/tekton the files to inject.
//
// The RolloutWorker (a River job) owns credential access through a Store
// (satisfied by lib/applications), exactly as lib/tekton's InstallWorker does:
// job args carry only ids, and the worker re-opens sealed credentials and mints
// any cloud token per attempt so retries self-heal.
package helm

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/tekton"
)

// Rollout lifecycle status values. They match the application.status ent enum;
// the worker reports progress through Store.MarkRollout using these so the
// persistence layer (lib/applications) maps them to the column without the
// worker importing it (which would be a cycle — lib/applications imports
// lib/helm).
const (
	StatusPending      = "pending"
	StatusDeploying    = "deploying"
	StatusDeployed     = "deployed"
	StatusFailed       = "failed"
	StatusUninstalling = "uninstalling"
	StatusUninstalled  = "uninstalled"
)

// Actions a rollout job can carry. Deploy and Upgrade run the same idempotent
// `helm upgrade --install`; they differ only in the in-flight status reported.
// Preview is the dry-run sibling: the preview job runs the same setup but a
// `helm diff upgrade ... --detailed-exitcode` instead of an install, changing
// nothing — it reports through Store.MarkPreview using the Sync* values below.
const (
	ActionDeploy    = "deploy"
	ActionUpgrade   = "upgrade"
	ActionUninstall = "uninstall"
	ActionPreview   = "preview"
)

// Sync (preview/diff) status values. They match the application.sync_status ent
// enum; the preview worker reports through Store.MarkPreview using these, so the
// persistence layer maps them to the column without the worker importing
// lib/applications (which would be a cycle, like the Status* values above).
const (
	SyncUnknown    = "unknown"
	SyncRefreshing = "refreshing"
	SyncSynced     = "synced"
	SyncOutOfSync  = "out_of_sync"
	SyncError      = "error"
)

// Chart source kinds. Match the application.chart_source ent enum.
const (
	SourceHTTPRepo = "http_repo"
	SourceOCI      = "oci"
	SourceGit      = "git"
)

// Config keys for the source-specific, non-secret parameters stored on an
// application (application.config). Match what lib/applications validates and
// the UI collects.
const (
	ConfigRepoURL = "repo_url"
	ConfigChart   = "chart"
	ConfigVersion = "version"
	ConfigGitRef  = "git_ref"
	ConfigGitPath = "git_path"
)

// Keys within a values source — one element of Rollout.ValuesSources, a flat
// string map describing a git repo to pull a values file from. RepoURL and Path
// are required; GitRef is optional (default branch when empty). A private
// github.com source is authenticated by the same attached installation as the
// chart (HasGitToken) — the credential helper is wired once for the step.
const (
	ValuesSourceRepoURL = "repo_url"
	ValuesSourceGitRef  = "git_ref"
	ValuesSourcePath    = "path"
)

// File names injected into the rollout step (keys in tekton.RunSpec.Files),
// mounted under tekton.CredsMountPath. The registry files carry a private-chart
// credential (when one is attached); the script reads them at runtime so the
// password never appears in the script string, the TaskRun manifest, or env.
const (
	KubeconfigFile       = "kubeconfig"
	ValuesFile           = "values.yaml"
	RegistryUsernameFile = "registry-username"
	RegistryPasswordFile = "registry-password"
	// GitCredentialsFile carries a git credential line
	// (https://x-access-token:<token>@github.com) for a private-Git chart. It is
	// wired into git via `credential.helper store --file=<this>` so the token is
	// read from the mounted file at runtime and never lands in the script string,
	// the clone's argv, the TaskRun manifest, or the workspace's .git/config.
	GitCredentialsFile = "git-credentials"
)

// Rollout-protocol log markers: after cloning a git source the script echoes the
// resolved commit SHA on its own line, so the worker can record what a
// pull-on-deploy rollout actually resolved to (chart/values refs are mutable, so
// a branch can move between runs). ParseRevisions reads them back out of the
// captured run logs — the same logs already persisted for the run's history.
const (
	revChartPrefix = "SPACEFLEET_CHART_REVISION="
	// A values marker is per-source: "SPACEFLEET_VALUES_REVISION_<i>=<sha>", where
	// <i> is the source's index in Rollout.ValuesSources, so the worker can pair
	// each resolved SHA back to its source.
	revValuesPrefix = "SPACEFLEET_VALUES_REVISION_"
)

// Preview-protocol log markers. The diff body is bracketed by sentinels so
// ParseDiff can slice it out exactly rather than heuristically filtering setup
// chatter (plugin install, clones, repo add) from the captured logs. The changes
// marker carries `helm diff --detailed-exitcode`'s verdict (exit 2 ⇒ changes).
const (
	diffBeginMarker   = "SPACEFLEET_DIFF_BEGIN"
	diffEndMarker     = "SPACEFLEET_DIFF_END"
	diffChangesPrefix = "SPACEFLEET_DIFF_CHANGES="
)

// Revisions are the git commit SHAs a rollout resolved, parsed from its logs.
// Chart is empty when the chart was not a git clone (an http_repo/oci chart).
// Values maps a values-source index to its resolved SHA; empty when an app has
// no values-from-git sources.
type Revisions struct {
	Chart  string
	Values map[int]string
}

// ParseRevisions extracts the chart/values commit SHAs the rollout script echoed
// into its logs. The last occurrence of a given marker wins, so a retried clone
// within the same captured log reports the latest resolved SHA.
func ParseRevisions(logs string) Revisions {
	rev := Revisions{Values: map[int]string{}}
	for _, line := range strings.Split(logs, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, revChartPrefix); ok {
			rev.Chart = strings.TrimSpace(v)
			continue
		}
		if rest, ok := strings.CutPrefix(line, revValuesPrefix); ok {
			// rest is "<i>=<sha>".
			idxStr, sha, ok := strings.Cut(rest, "=")
			if !ok {
				continue
			}
			if i, err := strconv.Atoi(idxStr); err == nil {
				rev.Values[i] = strings.TrimSpace(sha)
			}
		}
	}
	return rev
}

// Diff is the result of a preview run, parsed from its logs: the `helm diff`
// body (between the begin/end sentinels, so setup chatter is excluded) and
// whether deploying would change the live cluster (the --detailed-exitcode
// verdict the script echoed).
type Diff struct {
	Body       string
	HasChanges bool
}

// ParseDiff extracts the preview's diff body and changes verdict from its
// captured logs. The body is the lines strictly between the begin and end
// sentinels; HasChanges reads the SPACEFLEET_DIFF_CHANGES marker (true ⇒ a
// deploy would change the cluster). A run missing the markers (e.g. it failed
// before the diff) yields an empty body and HasChanges=false.
func ParseDiff(logs string) Diff {
	var d Diff
	lines := strings.Split(logs, "\n")
	begin, end := -1, -1
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case diffBeginMarker:
			begin = i
		case diffEndMarker:
			end = i
		}
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), diffChangesPrefix); ok {
			d.HasChanges = strings.TrimSpace(v) == "true"
		}
	}
	if begin >= 0 && end > begin {
		d.Body = strings.Join(lines[begin+1:end], "\n")
	}
	return d
}

// DefaultImage is the helm CLI image the rollout step runs in. alpine/k8s
// bundles helm, kubectl, and git, so the git chart source works without an
// extra install. Pinned here; overridable later (per-app or per-deploy).
const DefaultImage = "alpine/k8s:1.31.1"

// helmDiffVersion pins the databus23/helm-diff plugin a preview run installs at
// step start (alpine/k8s doesn't bundle it). Installed per run for now; baking
// it into a custom runner image is the documented follow-up. Bump here.
const helmDiffVersion = "v3.9.13"

// WaitTimeout is the `helm --wait --timeout` value for a target reached via the
// given connection method. Cloud methods mint short-lived (~15-min) bearer
// tokens, so the injected kubeconfig would expire mid-wait — cap them under that
// TTL. The long-lived methods (token, kubeconfig, in_cluster SA) get a generous
// window. It is the *target* cluster's method that matters (the token being
// embedded in the injected kubeconfig).
func WaitTimeout(method k8s.Method) time.Duration {
	switch method {
	case k8s.MethodEKS, k8s.MethodGKE, k8s.MethodAKS:
		return 10 * time.Minute
	default:
		return 30 * time.Minute
	}
}

// Rollout is the inputs Script needs to render the helm shell script: the
// action, chart source + its config, release name, target namespace, and the
// wait timeout (from WaitTimeout(targetMethod)).
type Rollout struct {
	Action          string
	ChartSource     string
	Config          map[string]string
	ReleaseName     string
	TargetNamespace string
	WaitTimeout     time.Duration
	// ValuesSources are the values-from-git sources (orthogonal to the chart
	// source), each a flat map keyed by ValuesSource*. They are cloned and layered
	// with -f in order — earlier first — beneath the inline values, which wins.
	ValuesSources []map[string]string
	// HasCredential injects private-chart auth before the pull: for http_repo the
	// helm repo add gets --username/--password (from the mounted registry files);
	// for oci a `helm registry login --password-stdin` step runs first. The
	// credential's type is validated against ChartSource upstream, so the source
	// alone selects the injection. The values come from the mounted files, never
	// this script string.
	HasCredential bool
	// HasGitToken authenticates a private github.com clone — the chart (SourceGit),
	// the values-from-git source, or both: a git credential helper backed by the
	// mounted GitCredentialsFile is configured once before any clone, so the token
	// is read from the file at runtime rather than appearing in the script string,
	// the clone's argv, or the workspace .git/config. Set by the rollout resolver
	// when the app has a GitHub App installation attached.
	HasGitToken bool
}

// Script renders the shell script the rollout step runs. A deploy/upgrade
// targets the cluster via the injected kubeconfig and uses `--wait --timeout` so
// the step only succeeds once resources are Ready (so the rollout's
// success/failure is real). A preview (ActionPreview) runs the identical setup
// but a `helm diff` against the live cluster, changing nothing. `set -e` makes
// any intermediate failure fail the step.
func Script(r Rollout) string {
	kubeconfig := tekton.CredsMountPath + "/" + KubeconfigFile
	values := tekton.CredsMountPath + "/" + ValuesFile
	timeout := r.WaitTimeout.String()
	ns := r.TargetNamespace
	release := r.ReleaseName
	preview := r.Action == ActionPreview

	var b strings.Builder
	b.WriteString("#!/bin/sh\nset -e\n")

	if r.Action == ActionUninstall {
		// Idempotent for River retries: --ignore-not-found means re-running after
		// a successful uninstall still exits 0.
		fmt.Fprintf(&b, "helm uninstall %s -n %s --wait --timeout %s --ignore-not-found --kubeconfig %s\n",
			shQuote(release), shQuote(ns), shQuote(timeout), shQuote(kubeconfig))
		return b.String()
	}

	if preview {
		// alpine/k8s doesn't bundle helm-diff. Install it once (idempotent: skip if
		// already present, so a re-attached retry in the same pod doesn't trip set -e).
		fmt.Fprintf(&b, "helm plugin list 2>/dev/null | grep -q '^diff' || helm plugin install https://github.com/databus23/helm-diff --version %s\n", shQuote(helmDiffVersion))
	}

	version := r.Config[ConfigVersion]
	repoURL := r.Config[ConfigRepoURL]
	userFile := tekton.CredsMountPath + "/" + RegistryUsernameFile
	passFile := tekton.CredsMountPath + "/" + RegistryPasswordFile

	// Wire git's credential helper once, before any clone (the chart below, the
	// values source, or both), so a github.com clone reads the token from the
	// mounted file at runtime — never the script string, the clone's argv, or the
	// workspace .git/config. Hoisted out of the git chart case so a values-from-git
	// clone authenticates even when the chart comes from http_repo/oci.
	if r.HasGitToken {
		gitCredsFile := tekton.CredsMountPath + "/" + GitCredentialsFile
		fmt.Fprintf(&b, "git config --global credential.helper %s\n",
			shQuote("store --file="+gitCredsFile))
	}

	// Optional values-from-git sources (orthogonal to the chart source): clone each
	// to its own /values/<i> and collect a -f flag per source. They layer in order
	// — earlier first — all beneath the inline values, which wins.
	var valuesFiles []string
	for i, src := range r.ValuesSources {
		repo := src[ValuesSourceRepoURL]
		if repo == "" {
			continue
		}
		dir := "/values/" + strconv.Itoa(i)
		if ref := src[ValuesSourceGitRef]; ref != "" {
			fmt.Fprintf(&b, "git clone --depth 1 --branch %s %s %s\n", shQuote(ref), shQuote(repo), shQuote(dir))
		} else {
			fmt.Fprintf(&b, "git clone --depth 1 %s %s\n", shQuote(repo), shQuote(dir))
		}
		// Echo this source's resolved SHA (tagged with its index) so the worker can
		// pair it back to the source. A failed substitution yields an empty marker,
		// never a step failure under set -e (echo still exits 0).
		fmt.Fprintf(&b, "echo \"%s%d=$(git -C %s rev-parse HEAD)\"\n", revValuesPrefix, i, shQuote(dir))
		if p := src[ValuesSourcePath]; p != "" {
			// Containment guard: a `..` segment could escape the clone dir and read a
			// file elsewhere in the step (e.g. the mounted creds). shQuote blocks shell
			// injection but not traversal, so reject it outright.
			if hasTraversal(p) {
				fmt.Fprintf(&b, "echo 'invalid values path (path traversal not allowed): %s' >&2\nexit 1\n", p)
				return b.String()
			}
			valuesFiles = append(valuesFiles, dir+"/"+p)
		}
	}

	// addValueFlags appends the -f layering shared by both verbs: git-sourced values
	// are the base layers (in order); the inline values file is applied last so it
	// overrides them (helm merges -f left to right).
	addValueFlags := func() {
		for _, vf := range valuesFiles {
			fmt.Fprintf(&b, " -f %s", shQuote(vf))
		}
		fmt.Fprintf(&b, " -f %s --kubeconfig %s\n", shQuote(values), shQuote(kubeconfig))
	}

	// install emits the action's helm line for the given chart ref. A real rollout
	// runs `helm upgrade --install ... --wait`; a preview runs `helm diff upgrade
	// ... --detailed-exitcode` (the databus23/helm-diff plugin) against the live
	// cluster, changing nothing — bracketed by sentinels and with its exit code
	// captured so "there are changes" (exit 2) is reported as data, not a step
	// failure. No --create-namespace/--wait for a diff (it must not mutate, and a
	// missing namespace simply shows as all-additions).
	install := func(chartRef string) {
		if preview {
			b.WriteString("echo " + diffBeginMarker + "\n")
			b.WriteString("set +e\n")
			fmt.Fprintf(&b, "helm diff upgrade %s %s --install --three-way-merge --detailed-exitcode --no-color -C 5", shQuote(release), shQuote(chartRef))
			if version != "" {
				fmt.Fprintf(&b, " --version %s", shQuote(version))
			}
			fmt.Fprintf(&b, " -n %s", shQuote(ns))
			addValueFlags()
			b.WriteString("diff_rc=$?\nset -e\n")
			b.WriteString("echo " + diffEndMarker + "\n")
			// Exit 1 is a real diff failure; exit 2 means there are changes, exit 0
			// none — both are a successful run, so the step exits 0 and the verdict
			// rides out in the marker.
			b.WriteString("if [ \"$diff_rc\" -eq 1 ]; then echo 'helm diff failed' >&2; exit 1; fi\n")
			fmt.Fprintf(&b, "if [ \"$diff_rc\" -eq 2 ]; then echo '%strue'; else echo '%sfalse'; fi\n", diffChangesPrefix, diffChangesPrefix)
			b.WriteString("exit 0\n")
			return
		}
		fmt.Fprintf(&b, "helm upgrade --install %s %s", shQuote(release), shQuote(chartRef))
		if version != "" {
			fmt.Fprintf(&b, " --version %s", shQuote(version))
		}
		fmt.Fprintf(&b, " -n %s --create-namespace --wait --timeout %s", shQuote(ns), shQuote(timeout))
		addValueFlags()
	}

	switch r.ChartSource {
	case SourceHTTPRepo:
		if r.HasCredential {
			// Read the username/password from the mounted files at runtime so the
			// secret stays out of this script string and the TaskRun manifest.
			fmt.Fprintf(&b, "helm repo add r %s --username \"$(cat %s)\" --password \"$(cat %s)\"\n",
				shQuote(repoURL), shQuote(userFile), shQuote(passFile))
		} else {
			fmt.Fprintf(&b, "helm repo add r %s\n", shQuote(repoURL))
		}
		b.WriteString("helm repo update r\n")
		install("r/" + r.Config[ConfigChart])
	case SourceOCI:
		if r.HasCredential {
			// --password-stdin keeps the password off the process args entirely; the
			// password file is redirected as stdin.
			fmt.Fprintf(&b, "helm registry login %s --username \"$(cat %s)\" --password-stdin < %s\n",
				shQuote(ociRegistryHost(repoURL)), shQuote(userFile), shQuote(passFile))
		}
		install(repoURL)
	case SourceGit:
		// The git credential helper (for a private repo) is wired once above, before
		// any clone, so the chart clone here just reuses it.
		ref := r.Config[ConfigGitRef]
		if ref != "" {
			fmt.Fprintf(&b, "git clone --depth 1 --branch %s %s /src\n", shQuote(ref), shQuote(repoURL))
		} else {
			fmt.Fprintf(&b, "git clone --depth 1 %s /src\n", shQuote(repoURL))
		}
		// Echo the resolved chart SHA so the worker records what this run pulled.
		fmt.Fprintf(&b, "echo \"%s$(git -C /src rev-parse HEAD)\"\n", revChartPrefix)
		chartDir := "/src"
		if p := r.Config[ConfigGitPath]; p != "" {
			// Containment guard: reject a `..` segment so the chart dir can't escape
			// the clone (shQuote blocks shell injection but not path traversal).
			if hasTraversal(p) {
				fmt.Fprintf(&b, "echo 'invalid git_path (path traversal not allowed): %s' >&2\nexit 1\n", p)
				return b.String()
			}
			chartDir = "/src/" + p
		}
		fmt.Fprintf(&b, "cd %s\n", shQuote(chartDir))
		b.WriteString("helm dependency build\n")
		install(".")
	default:
		// Unknown source: emit a script that fails clearly rather than silently
		// doing nothing. lib/applications validates the source on create, so this
		// is defensive.
		fmt.Fprintf(&b, "echo 'unknown chart source: %s' >&2\nexit 1\n", r.ChartSource)
	}
	return b.String()
}

// shQuote single-quotes a value for safe interpolation into the /bin/sh script,
// escaping any embedded single quotes.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hasTraversal reports whether p contains a ".." path segment, so a chart/values
// path can't escape its clone directory. It checks segments (not a substring) so
// a legitimate name like "..foo" or "foo..bar" is allowed; only a bare ".."
// component is rejected. Backslashes are normalized to forward slashes first
// since the path is used in a POSIX shell but may arrive Windows-style.
func hasTraversal(p string) bool {
	p = strings.ReplaceAll(p, "\\", "/")
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// ociRegistryHost extracts the registry host `helm registry login` expects from
// an OCI chart reference like "oci://registry-1.docker.io/org/chart": the scheme
// is stripped and only the host (up to the first path separator) is kept.
func ociRegistryHost(repoURL string) string {
	s := strings.TrimPrefix(repoURL, "oci://")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return s
}
