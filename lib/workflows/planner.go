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
	"github.com/spacefleet/spacefleet/lib/tofu"
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
// (for the app-level runner cluster), the snapshot node (its as-run config + its
// own deploy target, for the cluster-deploying types), the run action, the
// per-run force flag (a deploy-only "roll the workload" opt-in, threaded from the
// River job args so it survives retries), and the persisted run name to re-attach
// to. It is the workflow analogue of applications.ResolveRollout, sharing the exact
// same resolution layer (lib/deploy) so the credential/kubeconfig/token handling is
// not duplicated.
//
// The planner switches on the component type so adding a new type is a new case,
// not a rewrite. preview is a real action: each component is planned as its own
// per-component dry-run (helm diff / kubectl diff), and the worker clears the
// scheduler deps for a preview run so every component's dry-run is independently
// runnable and they preview concurrently rather than gating on upstream "passes".
func (w *WorkflowRunWorker) planComponent(ctx context.Context, app *ent.Application, node GraphNode, action string, force bool, existingRun string, runID uuid.UUID, byID map[uuid.UUID]GraphNode) (tekton.RunRequest, error) {
	switch node.Type {
	case TypeHelm:
		return w.planHelm(ctx, app, node, action, force, existingRun)
	case TypeManifest:
		// force is a helm-only "roll the workload" opt-in; a manifest apply has no
		// equivalent, so it's intentionally not threaded into planManifest.
		return w.planManifest(ctx, app, node, action, existingRun)
	case TypeTerraform:
		// force is helm-only; a terraform plan/apply has no equivalent. runID + byID
		// let planTofu derive the planfile-handover Secret and the shared backend
		// state identity it shares with its plan node.
		return w.planTofu(ctx, app, node, action, existingRun, runID, byID)
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

	// Runner is app-level; the target cluster lives on the component itself
	// (validateComponentTargets guarantees a manifest node names one).
	targetClusterID := deref(node.TargetClusterID)

	// A manifest must clone (and thus authenticate) for both deploy and uninstall —
	// uninstall needs the manifests to know what to delete. So request the git
	// token whenever an installation is attached, not only on deploy.
	pullsChart := node.GitHubInstallationID != nil && *node.GitHubInstallationID != uuid.Nil
	resolved, err := w.resolver.Resolve(ctx, deploy.RunInputs{
		OrgID:                app.OrganizationID,
		ApplicationID:        app.ID,
		ComponentID:          node.ID,
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
			Name:      manifestRunPrefix(node),
			Image:     helm.DefaultImage,
			Script:    script,
			Files:     resolved.Files,
			Env:       resolved.Env,
			SecretEnv: resolved.SecretEnv,
		},
	}, nil
}

// planTofu builds the OpenTofu RunSpec + runner connection for a terraform
// component. It runs on the app-level runner cluster, reads the command/backend
// from the component config, calls the shared resolver (lib/deploy) for the
// optional git token + cloud credential, and renders the plan/apply script via
// tofu.Script in tofu.DefaultImage.
//
// A terraform component has no cluster target, so no kubeconfig is injected and
// the implicit kubernetes state backend is unavailable — the component must name
// an explicit backend (enforced by validateTerraformConfig). PullsChart is true
// for every action (a terraform uninstall is a `tofu destroy` that still needs
// state + the root module) so a private-repo git token and a cloud credential
// are resolved regardless of action.
//
// The plan node's stdout is the review material captured as the component_run
// logs; the apply node applies the EXACT planfile the plan node produced,
// handed over through a Kubernetes Secret rather than re-planning — see the tofu
// package doc. The handover Secret and the backend state identity are both keyed
// off the plan node's id, so an apply node resolves its upstream plan node (via
// byID) and shares the plan's state and planfile.
func (w *WorkflowRunWorker) planTofu(ctx context.Context, app *ent.Application, node GraphNode, action, existingRun string, runID uuid.UUID, byID map[uuid.UUID]GraphNode) (tekton.RunRequest, error) {
	tofuAction, err := tofuActionFor(action)
	if err != nil {
		return tekton.RunRequest{}, err
	}

	// Resolve the plan node id this component is bound to: a plan node is its own
	// reference; an apply node points at its single upstream terraform plan node
	// (guaranteed by validateWorkflow). Both the backend state Secret and the
	// planfile-handover Secret are keyed off it so plan and apply share state and
	// the apply applies the plan's saved planfile.
	planID := node.ID
	if node.Config[terraformConfigCommand] == terraformCommandApply {
		planID = upstreamTofuPlanID(node, byID)
	}

	// The planfile-handover Secret name. Left empty for a preview (read-only, no
	// planfile) or when an apply node has no upstream plan (defensive: the script
	// then fails closed). Per-run so concurrent runs don't collide, and stable
	// across an approval pause (the run id is unchanged on resume).
	var planArtifactSecret string
	if action != ActionPreview && planID != uuid.Nil {
		planArtifactSecret = tofuPlanArtifactSecret(runID, planID)
	}

	// Decode an optional custom backend_config (validated at write time in
	// validateTerraformConfig); ignored for the kubernetes default backend.
	backendConfig, err := decodeBackendConfig(node.Config[terraformConfigBackendConfig])
	if err != nil {
		return tekton.RunRequest{}, fmt.Errorf("workflows: component %q: %w", node.Name, err)
	}

	// Backend mode + an optional cloud credential (byo mode). Validation already
	// gated these at write time, so a blank cloud_credential_id parses to uuid.Nil
	// and any parse error is ignored here (it can't occur for a validated config).
	backendMode := node.Config[terraformConfigBackendMode]
	cloudCredentialID, _ := uuid.Parse(node.Config[terraformConfigCloudCredentialID])

	// A terraform component has no cluster target (TargetClusterID is uuid.Nil), so
	// the resolver injects no kubeconfig — the state backend must be explicit
	// (validated at write time). PullsChart stays true so the git-credentials file
	// (private github.com repo) and the cloud credential are still resolved for
	// every action — even an uninstall (tofu destroy) needs state + the root module.
	pullsChart := true
	resolved, err := w.resolver.Resolve(ctx, deploy.RunInputs{
		OrgID:                app.OrganizationID,
		ApplicationID:        app.ID,
		ComponentID:          node.ID,
		RunnerClusterID:      app.RunnerClusterID,
		TargetClusterID:      uuid.Nil,
		Values:               "",
		ChartCredentialID:    uuid.Nil,
		GitHubInstallationID: deref(node.GitHubInstallationID),
		CloudCredentialID:    cloudCredentialID,
		PullsChart:           pullsChart,
	})
	if err != nil {
		return tekton.RunRequest{}, err
	}

	script := tofu.Script(tofu.Apply{
		Command:            node.Config[terraformConfigCommand],
		Action:             tofuAction,
		RepoURL:            node.Config[helm.ConfigRepoURL],
		GitRef:             node.Config[helm.ConfigGitRef],
		Path:               node.Config[manifestConfigPath],
		Backend:            node.Config[terraformConfigBackend],
		BackendConfig:      backendConfig,
		Namespace:          helm.RunNamespace,
		HasGitToken:        resolved.HasGitToken,
		BackendMode:        backendMode,
		HasCloudAuth:       resolved.HasCloudAuth,
		PlanArtifactSecret: planArtifactSecret,
	})

	return tekton.RunRequest{
		Conn:        resolved.RunnerConn,
		Namespace:   helm.RunNamespace,
		ExistingRun: existingRun,
		Spec: tekton.RunSpec{
			Name:      tofuRunPrefix(node),
			Image:     tofu.DefaultImage,
			Script:    script,
			Files:     resolved.Files,
			Env:       resolved.Env,
			SecretEnv: resolved.SecretEnv,
		},
	}, nil
}

// tofuActionFor maps a workflow run action to the tofu script action. deploy →
// plan/apply; uninstall → plan -destroy / destroy; preview → a read-only plan;
// an unknown action returns a clear error rather than panicking — the executor
// surfaces it as a component failure, mirroring helm/manifest.
func tofuActionFor(action string) (string, error) {
	switch action {
	case ActionDeploy:
		return tofu.ActionDeploy, nil
	case ActionUninstall:
		return tofu.ActionUninstall, nil
	case ActionPreview:
		return tofu.ActionPreview, nil
	default:
		return "", fmt.Errorf("workflows: unknown run action %q", action)
	}
}

// decodeBackendConfig parses the JSON object a terraform component stores under
// the backend_config key into a flat string map for the script renderer. An
// empty or absent value yields nil (the kubernetes default backend needs none).
// Values are stringified so the renderer can emit them as HCL strings.
func decodeBackendConfig(encoded string) (map[string]string, error) {
	if encoded == "" {
		return nil, nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(encoded), &raw); err != nil {
		return nil, fmt.Errorf("invalid %s (expected a JSON object): %w", terraformConfigBackendConfig, err)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out, nil
}

// tofuPlanArtifactSecret is the name of the Kubernetes Secret the plan node
// hands its saved planfile to its apply node through, per workflow run + plan
// node id. Keying on the run id isolates concurrent/sequential runs from each
// other and stays stable across an approval pause (the run id is unchanged when
// the run resumes), so the apply node — possibly in a later worker after the
// resume — reads exactly the plan node's planfile. Must be a DNS-1123 label.
func tofuPlanArtifactSecret(runID, planID uuid.UUID) string {
	return sanitizeLabel("tfplan-" + runID.String() + "-" + planID.String())
}

// upstreamTofuPlanID returns the id of the terraform plan node an apply node
// depends on (its expanded depends_on holds component-level edges, with groups
// already desugared). validateWorkflow guarantees an apply node has exactly one
// such upstream plan node; a missing one (defensive) yields uuid.Nil, which
// leaves the planfile Secret unset so the apply script fails closed.
func upstreamTofuPlanID(node GraphNode, byID map[uuid.UUID]GraphNode) uuid.UUID {
	for _, dep := range node.DependsOn {
		d, ok := byID[dep]
		if ok && d.Type == TypeTerraform && d.Config[terraformConfigCommand] == terraformCommandPlan {
			return d.ID
		}
	}
	return uuid.Nil
}

// tofuRunPrefix is the TaskRun generateName prefix for a terraform component (a
// DNS-1123 label), derived from the component name; lib/tekton appends a unique
// suffix.
func tofuRunPrefix(node GraphNode) string {
	return "tofu-" + sanitizeLabel(node.Name)
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
// the injected Files + auth flags, and renders the script via helm.Script. force
// is honored only for a deploy (it's meaningless for uninstall/preview).
func (w *WorkflowRunWorker) planHelm(ctx context.Context, app *ent.Application, node GraphNode, action string, force bool, existingRun string) (tekton.RunRequest, error) {
	helmAction, err := helmActionFor(action)
	if err != nil {
		return tekton.RunRequest{}, err
	}

	// Runner is app-level; the deploy target (cluster + namespace) lives on the
	// component itself — validateComponentTargets guarantees both are set for a
	// helm node before a run can begin.
	targetClusterID := deref(node.TargetClusterID)
	targetNamespace := node.TargetNamespace

	chartSource := node.Config[helmConfigChartSource]
	valuesSources, err := decodeValuesSources(node.Config[helmConfigValuesSources])
	if err != nil {
		return tekton.RunRequest{}, fmt.Errorf("workflows: component %q: %w", node.Name, err)
	}

	pullsChart := helmAction != helm.ActionUninstall
	resolved, err := w.resolver.Resolve(ctx, deploy.RunInputs{
		OrgID:                app.OrganizationID,
		ApplicationID:        app.ID,
		ComponentID:          node.ID,
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
		// Force a workload roll only when the run opted in (the per-run Force toggle,
		// carried on the River job args so it survives retries) and only for a deploy:
		// an uninstall has no upgrade and a preview is a dry-run, so force is moot for
		// both. Off by default keeps a plain deploy an idempotent `helm upgrade
		// --install` that doesn't churn pods when the rendered manifests are unchanged.
		Force: force && helmAction == helm.ActionDeploy,
	})

	return tekton.RunRequest{
		Conn:        resolved.RunnerConn,
		Namespace:   helm.RunNamespace,
		ExistingRun: existingRun,
		Spec: tekton.RunSpec{
			Name:      componentRunPrefix(node),
			Image:     helm.DefaultImage,
			Script:    script,
			Files:     resolved.Files,
			Env:       resolved.Env,
			SecretEnv: resolved.SecretEnv,
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
