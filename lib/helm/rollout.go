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
const (
	ActionDeploy    = "deploy"
	ActionUpgrade   = "upgrade"
	ActionUninstall = "uninstall"
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

// DefaultImage is the helm CLI image the rollout step runs in. alpine/k8s
// bundles helm, kubectl, and git, so the git chart source works without an
// extra install. Pinned here; overridable later (per-app or per-deploy).
const DefaultImage = "alpine/k8s:1.31.1"

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
	// HasCredential injects private-chart auth before the pull: for http_repo the
	// helm repo add gets --username/--password (from the mounted registry files);
	// for oci a `helm registry login --password-stdin` step runs first. The
	// credential's type is validated against ChartSource upstream, so the source
	// alone selects the injection. The values come from the mounted files, never
	// this script string.
	HasCredential bool
	// HasGitToken authenticates a private-Git (SourceGit) clone: a git credential
	// helper backed by the mounted GitCredentialsFile is configured before the
	// clone, so the token is read from the file at runtime rather than appearing
	// in the script string, the clone's argv, or the workspace .git/config. Set
	// by the rollout resolver when the app has a GitHub App installation attached.
	HasGitToken bool
}

// Script renders the shell script the rollout step runs. All forms target the
// cluster via the injected kubeconfig and use `--wait --timeout` so the step
// only succeeds once resources are Ready (so the rollout's success/failure is
// real). `set -e` makes any intermediate failure fail the step.
func Script(r Rollout) string {
	kubeconfig := tekton.CredsMountPath + "/" + KubeconfigFile
	values := tekton.CredsMountPath + "/" + ValuesFile
	timeout := r.WaitTimeout.String()
	ns := r.TargetNamespace
	release := r.ReleaseName

	var b strings.Builder
	b.WriteString("#!/bin/sh\nset -e\n")

	if r.Action == ActionUninstall {
		// Idempotent for River retries: --ignore-not-found means re-running after
		// a successful uninstall still exits 0.
		fmt.Fprintf(&b, "helm uninstall %s -n %s --wait --timeout %s --ignore-not-found --kubeconfig %s\n",
			shQuote(release), shQuote(ns), shQuote(timeout), shQuote(kubeconfig))
		return b.String()
	}

	version := r.Config[ConfigVersion]
	repoURL := r.Config[ConfigRepoURL]
	userFile := tekton.CredsMountPath + "/" + RegistryUsernameFile
	passFile := tekton.CredsMountPath + "/" + RegistryPasswordFile

	// install is the shared `helm upgrade --install <release> <ref> [flags]` line;
	// chartRef and any prep differ per source.
	install := func(chartRef string) {
		fmt.Fprintf(&b, "helm upgrade --install %s %s", shQuote(release), shQuote(chartRef))
		if version != "" {
			fmt.Fprintf(&b, " --version %s", shQuote(version))
		}
		fmt.Fprintf(&b, " -n %s --create-namespace --wait --timeout %s -f %s --kubeconfig %s\n",
			shQuote(ns), shQuote(timeout), shQuote(values), shQuote(kubeconfig))
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
		if r.HasGitToken {
			// Wire git's credential-store helper to the mounted credentials file so
			// the token is read at clone time without ever appearing in argv or the
			// workspace .git/config. `helm dependency build` reuses the same helper.
			gitCredsFile := tekton.CredsMountPath + "/" + GitCredentialsFile
			fmt.Fprintf(&b, "git config --global credential.helper %s\n",
				shQuote("store --file="+gitCredsFile))
		}
		ref := r.Config[ConfigGitRef]
		if ref != "" {
			fmt.Fprintf(&b, "git clone --depth 1 --branch %s %s /src\n", shQuote(ref), shQuote(repoURL))
		} else {
			fmt.Fprintf(&b, "git clone --depth 1 %s /src\n", shQuote(repoURL))
		}
		chartDir := "/src"
		if p := r.Config[ConfigGitPath]; p != "" {
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
