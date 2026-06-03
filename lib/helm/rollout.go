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
// mounted under tekton.CredsMountPath.
const (
	KubeconfigFile = "kubeconfig"
	ValuesFile     = "values.yaml"
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
		fmt.Fprintf(&b, "helm repo add r %s\n", shQuote(repoURL))
		b.WriteString("helm repo update r\n")
		install("r/" + r.Config[ConfigChart])
	case SourceOCI:
		install(repoURL)
	case SourceGit:
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
