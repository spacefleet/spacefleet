// Package manifest renders the shell script a manifest-apply workflow component
// runs: clone a git repo at a ref and `kubectl apply` (or `delete`) the
// manifests under a path against the target cluster's injected kubeconfig. Like
// lib/helm, Go never runs kubectl itself — it only emits the /bin/sh script and
// lets lib/tekton inject the credential files (the target kubeconfig and, for a
// private repo, the git-credentials line). This package is a pure renderer: it
// has no I/O and no ent dependency, so it is unit tested with plain string
// assertions, mirroring lib/helm/rollout.go's Script.
//
// The runner image requirement (kubectl + git) is already satisfied: the
// planner runs this step in helm.DefaultImage ("alpine/k8s:..."), which bundles
// helm, kubectl, and git — so no Docker/runner-image change is needed for the
// manifest type.
package manifest

import (
	"fmt"
	"strings"

	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/tekton"
)

// Actions a manifest component can run. Deploy applies the manifests; uninstall
// deletes them; preview is a read-only `kubectl diff` dry-run. These mirror the
// workflow run actions the planner maps from.
const (
	ActionDeploy    = "deploy"
	ActionUninstall = "uninstall"
	ActionPreview   = "preview"
)

// File names injected into the manifest step (keys in tekton.RunSpec.Files),
// mounted under tekton.CredsMountPath. They match the helm renderer's names
// because the shared resolver (lib/deploy) assembles the same Files map for both
// types: the target kubeconfig always, and a git-credentials line for a private
// github.com clone. Kept as local constants so this package does not import
// lib/helm (which would risk an import cycle and couples two renderers).
const (
	// KubeconfigFile is the injected kubeconfig for the target cluster; every
	// kubectl invocation targets it via --kubeconfig.
	KubeconfigFile = "kubeconfig"
	// GitCredentialsFile carries a git credential line
	// (https://x-access-token:<token>@github.com) for a private-Git repo. It is
	// wired into git via `credential.helper store --file=<this>` so the token is
	// read from the mounted file at runtime and never lands in the script string,
	// the clone's argv, the TaskRun manifest, or the workspace's .git/config.
	GitCredentialsFile = "git-credentials"
)

// revChartPrefix is the resolved-commit log marker the script echoes after the
// clone. It is intentionally the SAME string lib/helm uses (helm's unexported
// revChartPrefix) so the worker's existing helm.ParseRevisions call captures a
// manifest component's resolved SHA into chart_revision unchanged — no worker
// change. lib/helm's revision marker is unexported (unlike the diff markers,
// which this package reuses via helm.Diff*Marker), so it is duplicated here; if
// lib/helm's marker ever changes, update this in lockstep.
const revChartPrefix = "SPACEFLEET_CHART_REVISION="

// Apply is the inputs Script needs to render the manifest shell script: the
// action, the git source (repo URL + optional ref), the path (file or directory)
// of manifests within the repo, and whether a git token was injected for a
// private clone.
type Apply struct {
	// Action is one of ActionDeploy / ActionUninstall / ActionPreview.
	Action string
	// RepoURL is the git repository to clone the manifests from.
	RepoURL string
	// GitRef is an optional branch/tag to clone (default branch when empty).
	GitRef string
	// Path is the file or directory within the repo to apply/delete. kubectl's
	// -f handles both a single file and a directory.
	Path string
	// HasGitToken authenticates a private github.com clone: a git credential
	// helper backed by the mounted GitCredentialsFile is configured once before
	// the clone, so the token is read from the file at runtime rather than
	// appearing in the script string, the clone's argv, or the workspace
	// .git/config. Set by the resolver when the component has a GitHub App
	// installation attached. Uninstall pulls a token too (it must still clone to
	// know what to delete), so it is honored for both actions.
	HasGitToken bool
}

// Script renders the /bin/sh script the manifest step runs. All verbs clone the
// repo first (so deletes/diffs target exactly what was applied) and echo the
// resolved SHA via the shared revision marker. Deploy runs `kubectl apply -f`;
// uninstall runs `kubectl delete -f --ignore-not-found` (idempotent for River
// retries); preview runs a read-only `kubectl diff -f`, bracketed by the SAME
// diff sentinels lib/helm emits so helm.ParseDiff slices the diff body out
// identically for both component types (the diff itself changes nothing, and
// "there are changes" is reported as data via the marker, not as a step failure).
// `set -e` makes any intermediate failure fail the step. The git token is never
// written into the script — only via the mounted credentials file + a credential
// helper, exactly as lib/helm does.
func Script(a Apply) string {
	kubeconfig := tekton.CredsMountPath + "/" + KubeconfigFile

	var b strings.Builder
	b.WriteString("#!/bin/sh\nset -e\n")

	// Containment guard: a `..` segment could escape the clone dir and read a file
	// elsewhere in the step (e.g. the mounted creds). shQuote blocks shell
	// injection but not traversal, so reject it outright before emitting any clone.
	if hasTraversal(a.Path) {
		fmt.Fprintf(&b, "echo 'invalid path (path traversal not allowed): %s' >&2\nexit 1\n", a.Path)
		return b.String()
	}

	// Wire git's credential helper once, before the clone, so a private github.com
	// clone reads the token from the mounted file at runtime — never the script
	// string, the clone's argv, or the workspace .git/config.
	if a.HasGitToken {
		gitCredsFile := tekton.CredsMountPath + "/" + GitCredentialsFile
		fmt.Fprintf(&b, "git config --global credential.helper %s\n",
			shQuote("store --file="+gitCredsFile))
	}

	// Clone the manifests (both verbs clone: uninstall needs the manifests to know
	// what to delete). --depth 1 keeps it shallow; an explicit ref pins the branch.
	if a.GitRef != "" {
		fmt.Fprintf(&b, "git clone --depth 1 --branch %s %s /src\n", shQuote(a.GitRef), shQuote(a.RepoURL))
	} else {
		fmt.Fprintf(&b, "git clone --depth 1 %s /src\n", shQuote(a.RepoURL))
	}
	// Echo the resolved SHA so the worker records what this run applied (a branch
	// can move between runs). Reuses the helm revision marker so the worker's
	// existing helm.ParseRevisions captures it as the component's revision.
	fmt.Fprintf(&b, "echo \"%s$(git -C /src rev-parse HEAD)\"\n", revChartPrefix)

	target := "/src/" + a.Path

	if a.Action == ActionPreview {
		// Read-only dry-run: `kubectl diff -f` against the live cluster, bracketed by
		// the diff sentinels (the SAME strings lib/helm emits, exported as
		// helm.Diff*Marker) so helm.ParseDiff slices the body out identically for both
		// types. kubectl diff exit codes: 0 = no diff, 1 = differences present, >1 =
		// error. The diff itself must NOT fail the step — "there are changes" is data,
		// carried by the changes marker — so capture the code under `set +e` and only
		// exit 1 on a real error (>1), mirroring helm.Script's preview exit handling.
		b.WriteString("echo " + helm.DiffBeginMarker + "\n")
		b.WriteString("set +e\n")
		fmt.Fprintf(&b, "kubectl diff -f %s --kubeconfig %s\n", shQuote(target), shQuote(kubeconfig))
		b.WriteString("diff_rc=$?\nset -e\n")
		b.WriteString("echo " + helm.DiffEndMarker + "\n")
		b.WriteString("if [ \"$diff_rc\" -gt 1 ]; then echo 'kubectl diff failed' >&2; exit 1; fi\n")
		fmt.Fprintf(&b, "if [ \"$diff_rc\" -eq 1 ]; then echo '%strue'; else echo '%sfalse'; fi\n", helm.DiffChangesPrefix, helm.DiffChangesPrefix)
		b.WriteString("exit 0\n")
		return b.String()
	}

	if a.Action == ActionUninstall {
		// Idempotent for River retries: --ignore-not-found means re-running after a
		// successful delete still exits 0.
		fmt.Fprintf(&b, "kubectl delete -f %s --ignore-not-found --kubeconfig %s\n",
			shQuote(target), shQuote(kubeconfig))
		return b.String()
	}

	// Deploy/upgrade: apply the manifests under the path (kubectl apply -f handles
	// both a single file and a directory).
	fmt.Fprintf(&b, "kubectl apply -f %s --kubeconfig %s\n",
		shQuote(target), shQuote(kubeconfig))
	return b.String()
}

// shQuote single-quotes a value for safe interpolation into the /bin/sh script,
// escaping any embedded single quotes. Replicated from lib/helm (where it is
// unexported) so this renderer stays self-contained.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hasTraversal reports whether p contains a ".." path segment, so the apply path
// can't escape the clone directory. It checks segments (not a substring) so a
// legitimate name like "..foo" or "foo..bar" is allowed; only a bare ".."
// component is rejected. Backslashes are normalized to forward slashes first
// since the path is used in a POSIX shell but may arrive Windows-style.
// Replicated from lib/helm (where it is unexported).
func hasTraversal(p string) bool {
	p = strings.ReplaceAll(p, "\\", "/")
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}
