package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/deploy"
	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/manifest"
	"github.com/spacefleet/spacefleet/lib/tekton"
)

// Component helm-config keys that live alongside the chart-source keys
// (helm.Config*) and chart_source (helmConfigChartSource) in a helm component's
// flat config map. They carry the per-component values, the git values sources,
// and an optional explicit release name — the workflow analogue of the columns
// the single-helm Application stored directly.
const (
	helmConfigValues        = "values"
	helmConfigValuesSources = "values_sources"
	helmConfigReleaseName   = "release_name"
)

// planComponent builds the run plan for one workflow node, given the application
// (for the app-level runner cluster + default target cluster/namespace), the
// snapshot node (its as-run config + per-component overrides), the run action,
// and the persisted run name to re-attach to. It is the workflow analogue of
// applications.ResolveRollout, sharing the exact same resolution layer
// (lib/deploy) so the credential/kubeconfig/token handling is not duplicated.
//
// The planner switches on the component type so adding a new type (manifest,
// later) is a new case, not a rewrite. preview is intentionally not supported in
// this phase: it returns a clear "not yet supported" error rather than panicking.
func (w *WorkflowRunWorker) planComponent(ctx context.Context, app *ent.Application, node GraphNode, action, existingRun string) (tekton.RunRequest, error) {
	switch node.Type {
	case TypeHelm:
		return w.planHelm(ctx, app, node, action, existingRun)
	case TypeManifest:
		return w.planManifest(ctx, app, node, action, existingRun)
	default:
		return tekton.RunRequest{}, fmt.Errorf("workflows: component %q has unsupported type %q for execution", node.Name, node.Type)
	}
}

// planManifest builds the manifest RunSpec + runner connection for a manifest
// component. It resolves the runner/target clusters (honoring the per-component
// target override — the namespace is informational for a manifest, since the
// manifests carry their own namespaces, but the target kubeconfig is what
// matters), decodes the git source from the component config, calls the shared
// resolver (lib/deploy) for the injected kubeconfig + optional git token, and
// renders the apply/delete script via manifest.Script.
//
// It reuses the helm resolver path with Values="" and ChartCredentialID=nil (a
// manifest has no chart credential): the resolver still injects the target
// kubeconfig, and — for a private github.com repo — the git-credentials file when
// a GitHub App installation is attached and PullsChart is true. PullsChart is set
// to (action != uninstall) to match the helm convention, but the manifest script
// clones on uninstall too (it needs the manifests to delete); the
// GitHubInstallationID is honored regardless via the same Files map, so a private
// uninstall still authenticates.
func (w *WorkflowRunWorker) planManifest(ctx context.Context, app *ent.Application, node GraphNode, action, existingRun string) (tekton.RunRequest, error) {
	manifestAction, err := manifestActionFor(action)
	if err != nil {
		return tekton.RunRequest{}, err
	}

	// Runner is always app-level; target cluster/namespace take the per-component
	// override when set, else the application's app-level default.
	targetClusterID := app.TargetClusterID
	if node.TargetClusterID != nil && *node.TargetClusterID != uuid.Nil {
		targetClusterID = *node.TargetClusterID
	}

	// A manifest must clone (and thus authenticate) for both deploy and uninstall —
	// uninstall needs the manifests to know what to delete. So request the git
	// token whenever an installation is attached, not only on deploy.
	pullsChart := node.GitHubInstallationID != nil && *node.GitHubInstallationID != uuid.Nil
	resolved, err := w.resolver.Resolve(ctx, deploy.RunInputs{
		OrgID:                app.OrganizationID,
		RunnerClusterID:      app.RunnerClusterID,
		TargetClusterID:      targetClusterID,
		Values:               "",
		ChartCredentialID:    uuid.Nil,
		GitHubInstallationID: deref(node.GitHubInstallationID),
		PullsChart:           pullsChart,
	})
	if err != nil {
		return tekton.RunRequest{}, err
	}

	script := manifest.Script(manifest.Apply{
		Action:      manifestAction,
		RepoURL:     node.Config[helm.ConfigRepoURL],
		GitRef:      node.Config[helm.ConfigGitRef],
		Path:        node.Config[manifestConfigPath],
		HasGitToken: resolved.HasGitToken,
	})

	return tekton.RunRequest{
		Conn:        resolved.RunnerConn,
		Namespace:   helm.RunNamespace,
		ExistingRun: existingRun,
		Spec: tekton.RunSpec{
			Name:   manifestRunPrefix(node),
			Image:  helm.DefaultImage,
			Script: script,
			Files:  resolved.Files,
		},
	}, nil
}

// manifestActionFor maps a workflow run action to the manifest script action.
// deploy → apply; uninstall → delete --ignore-not-found; preview → a read-only
// `kubectl diff` dry-run; an unknown action returns a clear error rather than
// panicking — the executor surfaces it as a component failure, mirroring helm.
func manifestActionFor(action string) (string, error) {
	switch action {
	case ActionDeploy:
		return manifest.ActionDeploy, nil
	case ActionUninstall:
		return manifest.ActionUninstall, nil
	case ActionPreview:
		return manifest.ActionPreview, nil
	default:
		return "", fmt.Errorf("workflows: unknown run action %q", action)
	}
}

// manifestRunPrefix is the TaskRun generateName prefix for a manifest component
// (a DNS-1123 label), derived from the component name; lib/tekton appends a
// unique suffix.
func manifestRunPrefix(node GraphNode) string {
	return "manifest-" + sanitizeLabel(node.Name)
}

// planHelm builds the helm RunSpec + runner connection for a helm component. It
// resolves the runner/target clusters (honoring the per-component target
// override), decodes the component's helm config, calls the shared resolver for
// the injected Files + auth flags, and renders the script via helm.Script.
func (w *WorkflowRunWorker) planHelm(ctx context.Context, app *ent.Application, node GraphNode, action, existingRun string) (tekton.RunRequest, error) {
	helmAction, err := helmActionFor(action)
	if err != nil {
		return tekton.RunRequest{}, err
	}

	// Runner is always app-level; target cluster/namespace take the per-component
	// override when set, else the application's app-level default.
	targetClusterID := app.TargetClusterID
	if node.TargetClusterID != nil && *node.TargetClusterID != uuid.Nil {
		targetClusterID = *node.TargetClusterID
	}
	targetNamespace := app.TargetNamespace
	if node.TargetNamespace != "" {
		targetNamespace = node.TargetNamespace
	}

	chartSource := node.Config[helmConfigChartSource]
	valuesSources, err := decodeValuesSources(node.Config[helmConfigValuesSources])
	if err != nil {
		return tekton.RunRequest{}, fmt.Errorf("workflows: component %q: %w", node.Name, err)
	}

	pullsChart := helmAction != helm.ActionUninstall
	resolved, err := w.resolver.Resolve(ctx, deploy.RunInputs{
		OrgID:                app.OrganizationID,
		RunnerClusterID:      app.RunnerClusterID,
		TargetClusterID:      targetClusterID,
		Values:               node.Config[helmConfigValues],
		ChartCredentialID:    deref(node.ChartCredentialID),
		GitHubInstallationID: deref(node.GitHubInstallationID),
		PullsChart:           pullsChart,
	})
	if err != nil {
		return tekton.RunRequest{}, err
	}

	script := helm.Script(helm.Rollout{
		Action:          helmAction,
		ChartSource:     chartSource,
		Config:          helmChartConfig(node.Config),
		ValuesSources:   valuesSources,
		ReleaseName:     componentReleaseName(node),
		TargetNamespace: targetNamespace,
		WaitTimeout:     helm.WaitTimeout(resolved.TargetMethod),
		HasCredential:   resolved.HasCredential,
		HasGitToken:     resolved.HasGitToken,
		// A workflow deploy is a plain idempotent `helm upgrade --install` — no forced
		// workload roll. The legacy single-helm path made force an explicit per-deploy
		// opt-in; the workflow builder has no such toggle yet, so defaulting it on would
		// churn pods on every run. Add a per-run force option later if needed.
		Force: false,
	})

	return tekton.RunRequest{
		Conn:        resolved.RunnerConn,
		Namespace:   helm.RunNamespace,
		ExistingRun: existingRun,
		Spec: tekton.RunSpec{
			Name:   componentRunPrefix(node),
			Image:  helm.DefaultImage,
			Script: script,
			Files:  resolved.Files,
		},
	}, nil
}

// helmActionFor maps a workflow run action to the helm script action. deploy →
// helm deploy (upgrade --install); uninstall → helm uninstall; preview → helm
// diff (a read-only dry-run); an unknown action returns a clear error rather than
// panicking — the executor surfaces it as a component failure.
func helmActionFor(action string) (string, error) {
	switch action {
	case ActionDeploy:
		return helm.ActionDeploy, nil
	case ActionUninstall:
		return helm.ActionUninstall, nil
	case ActionPreview:
		return helm.ActionPreview, nil
	default:
		return "", fmt.Errorf("workflows: unknown run action %q", action)
	}
}

// helmChartConfig narrows a component's flat config to just the chart-source
// keys helm.Script reads (helm.Config*), leaving out the workflow-only keys
// (chart_source, values, values_sources, release_name) so they don't leak into
// the script's Config lookups.
func helmChartConfig(cfg map[string]string) map[string]string {
	out := make(map[string]string, len(cfg))
	for _, k := range []string{helm.ConfigRepoURL, helm.ConfigChart, helm.ConfigVersion, helm.ConfigGitRef, helm.ConfigGitPath} {
		if v, ok := cfg[k]; ok {
			out[k] = v
		}
	}
	return out
}

// decodeValuesSources parses the JSON-encoded []map[string]string a helm
// component stores under the values_sources key (the canvas serializes the
// ordered git values sources there, since config is a flat string map). An empty
// or absent value yields nil (inline-only values).
func decodeValuesSources(encoded string) ([]map[string]string, error) {
	if encoded == "" {
		return nil, nil
	}
	var sources []map[string]string
	if err := json.Unmarshal([]byte(encoded), &sources); err != nil {
		return nil, fmt.Errorf("invalid %s (expected a JSON array of objects): %w", helmConfigValuesSources, err)
	}
	return sources, nil
}

// componentReleaseName is the helm release name for a component: the explicit
// release_name config when set, else the component's name.
func componentReleaseName(node GraphNode) string {
	if rn := node.Config[helmConfigReleaseName]; rn != "" {
		return rn
	}
	return node.Name
}

// componentRunPrefix is the TaskRun generateName prefix for a component (a
// DNS-1123 label), derived from the release name; lib/tekton appends a unique
// suffix.
func componentRunPrefix(node GraphNode) string {
	return "helm-" + sanitizeLabel(componentReleaseName(node))
}

// deref returns the pointed-to id, or uuid.Nil for a nil pointer.
func deref(p *uuid.UUID) uuid.UUID {
	if p == nil {
		return uuid.Nil
	}
	return *p
}

// sanitizeLabel lowercases s and replaces any non-DNS-1123-label character with a
// hyphen, trimming leading/trailing hyphens, so it's a valid TaskRun generateName
// component. An empty result falls back to "release". Mirrors the helper in
// lib/applications (the run-name shape is identical for both paths).
func sanitizeLabel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "release"
	}
	return out
}
