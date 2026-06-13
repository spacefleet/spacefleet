package workflows

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/deploy"
	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/manifest"
	"github.com/spacefleet/spacefleet/lib/tekton"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

func TestHelmActionFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		action  string
		want    string
		wantErr bool
	}{
		{ActionDeploy, helm.ActionDeploy, false},
		{ActionUninstall, helm.ActionUninstall, false},
		{ActionPreview, helm.ActionPreview, false},
		{"bogus", "", true},
	}
	for _, c := range cases {
		got, err := helmActionFor(c.action)
		if c.wantErr {
			if err == nil {
				t.Errorf("helmActionFor(%q): expected error", c.action)
			}
			continue
		}
		if err != nil {
			t.Errorf("helmActionFor(%q): unexpected error %v", c.action, err)
		}
		if got != c.want {
			t.Errorf("helmActionFor(%q) = %q, want %q", c.action, got, c.want)
		}
	}
}

func TestManifestActionFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		action  string
		want    string
		wantErr bool
	}{
		{ActionDeploy, manifest.ActionDeploy, false},
		{ActionUninstall, manifest.ActionUninstall, false},
		{ActionPreview, manifest.ActionPreview, false},
		{"bogus", "", true},
	}
	for _, c := range cases {
		got, err := manifestActionFor(c.action)
		if c.wantErr {
			if err == nil {
				t.Errorf("manifestActionFor(%q): expected error", c.action)
			}
			continue
		}
		if err != nil {
			t.Errorf("manifestActionFor(%q): unexpected error %v", c.action, err)
		}
		if got != c.want {
			t.Errorf("manifestActionFor(%q) = %q, want %q", c.action, got, c.want)
		}
	}
}

func TestManifestRunPrefix(t *testing.T) {
	t.Parallel()
	if got := manifestRunPrefix(GraphNode{Name: "My Manifests"}); got != "manifest-my-manifests" {
		t.Fatalf("manifestRunPrefix: got %q, want %q", got, "manifest-my-manifests")
	}
	// Empty/garbage name falls back via sanitizeLabel.
	if got := manifestRunPrefix(GraphNode{Name: "  "}); got != "manifest-release" {
		t.Fatalf("manifestRunPrefix fallback: got %q", got)
	}
}

func TestDecodeValuesSources(t *testing.T) {
	t.Parallel()
	// Empty / absent → nil.
	if got, err := decodeValuesSources(""); err != nil || got != nil {
		t.Fatalf("empty: got %v err %v, want nil nil", got, err)
	}
	// Valid JSON array of objects.
	in := `[{"repo_url":"https://github.com/x/y","path":"values.yaml"},{"repo_url":"https://github.com/a/b","git_ref":"main","path":"prod.yaml"}]`
	got, err := decodeValuesSources(in)
	if err != nil {
		t.Fatalf("valid: unexpected error %v", err)
	}
	want := []map[string]string{
		{"repo_url": "https://github.com/x/y", "path": "values.yaml"},
		{"repo_url": "https://github.com/a/b", "git_ref": "main", "path": "prod.yaml"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("valid: got %v, want %v", got, want)
	}
	// Malformed JSON → error.
	if _, err := decodeValuesSources("{not json"); err == nil {
		t.Fatal("malformed: expected error")
	}
}

func TestHelmChartConfig(t *testing.T) {
	t.Parallel()
	cfg := map[string]string{
		helmConfigChartSource:   helm.SourceHTTPRepo,
		helm.ConfigRepoURL:      "https://charts.example.com",
		helm.ConfigChart:        "nginx",
		helm.ConfigVersion:      "1.2.3",
		helmConfigValues:        "replicas: 2",
		helmConfigValuesSources: "[]",
		helmConfigReleaseName:   "web",
	}
	got := helmChartConfig(cfg)
	// Only the chart-source keys should survive; the workflow-only keys must not
	// leak into helm.Script's Config lookups.
	want := map[string]string{
		helm.ConfigRepoURL: "https://charts.example.com",
		helm.ConfigChart:   "nginx",
		helm.ConfigVersion: "1.2.3",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("helmChartConfig: got %v, want %v", got, want)
	}
}

func TestComponentReleaseNameAndPrefix(t *testing.T) {
	t.Parallel()
	// Explicit release_name wins — the app name is not prefixed onto it.
	n := GraphNode{Name: "web", Config: map[string]string{helmConfigReleaseName: "explicit-release"}}
	if got := componentReleaseName("shop", n); got != "explicit-release" {
		t.Fatalf("release name with explicit: got %q", got)
	}
	// The run prefix tracks the component name, not the release name.
	if got := componentRunPrefix(n); got != "helm-web" {
		t.Fatalf("prefix with explicit: got %q", got)
	}
	// Falls back to <app>-<component>.
	n2 := GraphNode{Name: "web", Config: map[string]string{}}
	if got := componentReleaseName("shop", n2); got != "shop-web" {
		t.Fatalf("release name fallback: got %q", got)
	}
	if got := componentRunPrefix(n2); got != "helm-web" {
		t.Fatalf("prefix fallback: got %q", got)
	}
	// A legacy snapshot name (pre-slug) is still sanitized for the run name.
	if got := componentRunPrefix(GraphNode{Name: "My App"}); got != "helm-my-app" {
		t.Fatalf("prefix sanitizes legacy name: got %q", got)
	}
}

func TestUpstreamTofuPlanID(t *testing.T) {
	t.Parallel()
	planID := uuid.New()
	helmID := uuid.New()
	plan := GraphNode{ID: planID, Type: TypeTerraform, Config: map[string]string{terraformConfigCommand: terraformCommandPlan}}
	helmDep := GraphNode{ID: helmID, Type: TypeHelm, Config: map[string]string{}}
	byID := map[uuid.UUID]GraphNode{planID: plan, helmID: helmDep}

	// Apply depending on a helm gate AND its plan resolves to the plan id.
	apply := GraphNode{ID: uuid.New(), Type: TypeTerraform, DependsOn: []uuid.UUID{helmID, planID}, Config: map[string]string{terraformConfigCommand: terraformCommandApply}}
	if got := upstreamTofuPlanID(apply, byID); got != planID {
		t.Errorf("upstream plan id: got %s, want %s", got, planID)
	}

	// No upstream plan → uuid.Nil (the script then fails closed).
	orphan := GraphNode{ID: uuid.New(), Type: TypeTerraform, DependsOn: []uuid.UUID{helmID}, Config: map[string]string{terraformConfigCommand: terraformCommandApply}}
	if got := upstreamTofuPlanID(orphan, byID); got != uuid.Nil {
		t.Errorf("no upstream plan: got %s, want Nil", got)
	}
}

func TestTofuPlanArtifactSecret(t *testing.T) {
	t.Parallel()
	runID, planID := uuid.New(), uuid.New()
	name := tofuPlanArtifactSecret(runID, planID)
	// Stable for the same (run, plan) — so plan stores and apply fetches the same
	// Secret, including across an approval pause (same run id on resume).
	if again := tofuPlanArtifactSecret(runID, planID); again != name {
		t.Fatalf("not stable: %q vs %q", name, again)
	}
	// Per-run: a different run id yields a different Secret.
	if other := tofuPlanArtifactSecret(uuid.New(), planID); other == name {
		t.Fatalf("different runs must not share a planfile Secret: %q", other)
	}
	if name == "" || name != sanitizeLabel(name) {
		t.Fatalf("secret name is not a sanitized label: %q", name)
	}
}

func TestWithNativeLocking(t *testing.T) {
	t.Parallel()

	native, ok := tofu.ResolveVersion("1.12")
	if !ok || !native.NativeS3Lock {
		t.Fatal("test premise: 1.12 must be a native-locking line")
	}
	old, ok := tofu.ResolveVersion("1.9")
	if !ok || old.NativeS3Lock {
		t.Fatal("test premise: 1.9 must not be a native-locking line")
	}

	base := map[string]string{"bucket": "b", "key": "k", "region": "us-east-1"}

	// A native-locking line gets use_lockfile=true injected — without mutating
	// the caller's map.
	got := withNativeLocking(base, tofu.BackendS3, native)
	if got[s3BackendKeyLockfile] != "true" {
		t.Errorf("native line: use_lockfile = %q, want true", got[s3BackendKeyLockfile])
	}
	if _, mutated := base[s3BackendKeyLockfile]; mutated {
		t.Error("withNativeLocking mutated the input map")
	}

	// Everything else passes through unchanged (incl. dynamodb_table — running
	// both locks is OpenTofu's migration posture).
	withTable := map[string]string{"bucket": "b", "key": "k", "region": "us-east-1", s3BackendKeyDynamoTable: "tf-locks"}
	got = withNativeLocking(withTable, tofu.BackendS3, native)
	if got[s3BackendKeyDynamoTable] != "tf-locks" || got[s3BackendKeyLockfile] != "true" {
		t.Errorf("native line + dynamodb_table: got %v, want both locks", got)
	}

	// An explicit use_lockfile (API author's escape hatch) is respected.
	explicit := map[string]string{"bucket": "b", "key": "k", "region": "us-east-1", s3BackendKeyLockfile: "false"}
	got = withNativeLocking(explicit, tofu.BackendS3, native)
	if got[s3BackendKeyLockfile] != "false" {
		t.Errorf("explicit opt-out overridden: use_lockfile = %q", got[s3BackendKeyLockfile])
	}

	// Pre-1.10 lines are untouched — 1.9's s3 backend rejects the argument.
	got = withNativeLocking(base, tofu.BackendS3, old)
	if _, set := got[s3BackendKeyLockfile]; set {
		t.Errorf("1.9 line: use_lockfile must not be injected, got %v", got)
	}
}

// plannerConns is a ConnResolver stub for unit tests: every cluster resolves to
// a zero-value connection (planTofu's resolve path never dials it).
type plannerConns struct{}

func (plannerConns) ConnForTekton(context.Context, uuid.UUID, uuid.UUID) (k8s.Connection, error) {
	return k8s.Connection{}, nil
}

// TestPlanTofuProvisionsHandover: a non-preview plan or apply node provisions
// the pair's planfile-handover objects (Secret + scoped SA/Role/RoleBinding,
// via the ensureHandover seam) and runs the step as the same-named
// ServiceAccount; a preview does neither.
func TestPlanTofuProvisionsHandover(t *testing.T) {
	t.Parallel()
	runID, planID, applyID := uuid.New(), uuid.New(), uuid.New()
	app := &ent.Application{ID: uuid.New(), OrganizationID: uuid.New(), RunnerClusterID: uuid.New()}

	var ensured []string
	var ensuredLabels map[string]string
	w := &WorkflowRunWorker{
		resolver: deploy.NewResolver(plannerConns{}, nil, nil, nil, nil),
		ensureHandover: func(_ context.Context, _ k8s.Connection, namespace, name string, labels map[string]string) error {
			ensured = append(ensured, namespace+"/"+name)
			ensuredLabels = labels
			return nil
		},
	}

	planNode := GraphNode{
		ID: planID, ComponentID: planID, Name: "net", Type: TypeTerraform,
		Config: map[string]string{
			terraformConfigCommand: terraformCommandPlan,
			terraformConfigBackend: tofu.BackendS3,
		},
	}
	applyNode := GraphNode{
		ID: applyID, ComponentID: planID, Name: "net", Type: TypeTerraform,
		DependsOn: []uuid.UUID{planID},
		Config: map[string]string{
			terraformConfigCommand: terraformCommandApply,
			terraformConfigBackend: tofu.BackendS3,
		},
	}
	byID := map[uuid.UUID]GraphNode{planID: planNode, applyID: applyNode}
	secret := tofuPlanArtifactSecret(runID, planID)

	req, err := w.planTofu(context.Background(), app, planNode, ActionDeploy, "", runID, byID)
	if err != nil {
		t.Fatalf("planTofu(plan): %v", err)
	}
	if req.Spec.ServiceAccountName != secret {
		t.Errorf("plan node ServiceAccountName = %q, want %q", req.Spec.ServiceAccountName, secret)
	}
	if want := []string{tekton.JobsNamespace + "/" + secret}; !reflect.DeepEqual(ensured, want) {
		t.Errorf("ensured = %v, want %v", ensured, want)
	}
	wantLabels := map[string]string{
		tekton.RunOrgLabel: app.OrganizationID.String(),
		tekton.RunJobLabel: runID.String(),
	}
	if !reflect.DeepEqual(ensuredLabels, wantLabels) {
		t.Errorf("handover labels = %v, want %v", ensuredLabels, wantLabels)
	}

	// The apply node ensures the SAME pair objects (idempotent — heals a partial
	// provision) and runs as the same ServiceAccount, so it reads exactly the
	// Secret its plan node stored.
	ensured = nil
	req, err = w.planTofu(context.Background(), app, applyNode, ActionDeploy, "", runID, byID)
	if err != nil {
		t.Fatalf("planTofu(apply): %v", err)
	}
	if req.Spec.ServiceAccountName != secret {
		t.Errorf("apply node ServiceAccountName = %q, want %q", req.Spec.ServiceAccountName, secret)
	}
	if want := []string{tekton.JobsNamespace + "/" + secret}; !reflect.DeepEqual(ensured, want) {
		t.Errorf("apply ensured = %v, want %v", ensured, want)
	}

	// A preview is read-only: no planfile, so no handover objects and no
	// dedicated ServiceAccount (the pod needs no cluster access at all).
	ensured = nil
	req, err = w.planTofu(context.Background(), app, planNode, ActionPreview, "", runID, byID)
	if err != nil {
		t.Fatalf("planTofu(preview): %v", err)
	}
	if req.Spec.ServiceAccountName != "" {
		t.Errorf("preview ServiceAccountName = %q, want empty", req.Spec.ServiceAccountName)
	}
	if len(ensured) != 0 {
		t.Errorf("preview must not provision handover objects, got %v", ensured)
	}
}

// tokenConns resolves every cluster to a portable token-method connection so
// the resolver can build (never dial) a kubeconfig for it. The endpoint is a
// public host (the default endpoint policy rejects private/loopback).
type tokenConns struct{}

func (tokenConns) ConnForTekton(context.Context, uuid.UUID, uuid.UUID) (k8s.Connection, error) {
	return k8s.Connection{
		Method:      k8s.MethodToken,
		Endpoint:    "https://api.example.com",
		Credentials: []byte("a-bearer-token"),
	}, nil
}

// TestPlanTofuClusterAuth: an auth_cluster_id on the component rides the
// resolver's target path — the injected Files carry that cluster's kubeconfig
// and the script exports KUBE_CONFIG_PATH for the module's Kubernetes-backed
// providers. Without it, nothing kubeconfig-shaped reaches the step (the
// pre-existing behavior).
func TestPlanTofuClusterAuth(t *testing.T) {
	t.Parallel()
	runID, planID := uuid.New(), uuid.New()
	app := &ent.Application{ID: uuid.New(), OrganizationID: uuid.New(), RunnerClusterID: uuid.New()}
	w := &WorkflowRunWorker{
		resolver:       deploy.NewResolver(tokenConns{}, nil, nil, nil, nil),
		ensureHandover: func(context.Context, k8s.Connection, string, string, map[string]string) error { return nil },
	}

	withAuth := GraphNode{
		ID: planID, ComponentID: planID, Name: "net", Type: TypeTerraform,
		Config: map[string]string{
			terraformConfigCommand:       terraformCommandPlan,
			terraformConfigBackend:       tofu.BackendS3,
			terraformConfigAuthClusterID: uuid.New().String(),
		},
	}
	req, err := w.planTofu(context.Background(), app, withAuth, ActionDeploy, "", runID, map[uuid.UUID]GraphNode{planID: withAuth})
	if err != nil {
		t.Fatalf("planTofu(with auth): %v", err)
	}
	if _, ok := req.Spec.Files[helm.KubeconfigFile]; !ok {
		t.Errorf("cluster auth must inject the kubeconfig file, got Files keys %v", req.Spec.Files)
	}
	const exportLine = "export KUBE_CONFIG_PATH='/workspace/creds/kubeconfig'"
	if !strings.Contains(req.Spec.Script, exportLine) {
		t.Errorf("script must export KUBE_CONFIG_PATH\n---\n%s", req.Spec.Script)
	}

	withoutAuth := GraphNode{
		ID: planID, ComponentID: planID, Name: "net", Type: TypeTerraform,
		Config: map[string]string{
			terraformConfigCommand: terraformCommandPlan,
			terraformConfigBackend: tofu.BackendS3,
		},
	}
	req, err = w.planTofu(context.Background(), app, withoutAuth, ActionDeploy, "", runID, map[uuid.UUID]GraphNode{planID: withoutAuth})
	if err != nil {
		t.Fatalf("planTofu(without auth): %v", err)
	}
	if _, ok := req.Spec.Files[helm.KubeconfigFile]; ok {
		t.Errorf("no cluster auth must inject no kubeconfig, got Files keys %v", req.Spec.Files)
	}
	if strings.Contains(req.Spec.Script, "KUBE_CONFIG_PATH") {
		t.Errorf("no cluster auth must not export KUBE_CONFIG_PATH\n---\n%s", req.Spec.Script)
	}
}

// plannerVars is a VariableResolver stub returning fixed plain/secret maps for
// every component.
type plannerVars struct{ plain, secret map[string]string }

func (v plannerVars) ResolveEnv(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (map[string]string, map[string]string, error) {
	return v.plain, v.secret, nil
}

// helmInterpolationWorker builds a worker whose resolver serves a fixed
// variable set, for the planHelm interpolation tests.
func helmInterpolationWorker(plain, secret map[string]string) *WorkflowRunWorker {
	return &WorkflowRunWorker{
		resolver: deploy.NewResolver(tokenConns{}, nil, nil, nil, plannerVars{plain: plain, secret: secret}),
	}
}

// helmInterpolationNode builds a git-source helm node whose values, namespace,
// and release name carry interpolation references.
func helmInterpolationNode() GraphNode {
	target := uuid.New()
	return GraphNode{
		ID: uuid.New(), ComponentID: uuid.New(), Name: "web", Type: TypeHelm,
		TargetClusterID: &target,
		TargetNamespace: "customer-${{ vars.CUSTOMER_ID }}",
		Config: map[string]string{
			helmConfigChartSource: helm.SourceGit,
			helm.ConfigRepoURL:    "https://github.com/org/charts.git",
			helm.ConfigGitRef:     "main",
			helmConfigValues: "host: ${{ vars.CUSTOMER_ID }}.example.com\n" +
				"password: ${{ vars.DB_PASSWORD }}\n" +
				"tag: ${{ run.git_sha_short }}\n" +
				"full: ${{ run.git_sha }}\n" +
				"run: ${{ run.id }}\n" +
				"ref: ${{ run.git_ref }}\n",
			helmConfigReleaseName: "web-${{ vars.CUSTOMER_ID }}",
		},
	}
}

// TestPlanHelmInterpolation: the planner renders vars.* and run.* references in
// the inline values (into the mounted Files entry), the target namespace, and
// the release name; run.git_sha* render to the in-script sentinels and flip
// RenderGitContext (visible as the script's sed render).
func TestPlanHelmInterpolation(t *testing.T) {
	t.Parallel()
	runID := uuid.New()
	app := &ent.Application{ID: uuid.New(), OrganizationID: uuid.New(), RunnerClusterID: uuid.New()}
	w := helmInterpolationWorker(
		map[string]string{"CUSTOMER_ID": "acme"},
		map[string]string{"DB_PASSWORD": "s3cret"},
	)

	req, err := w.planHelm(context.Background(), app, helmInterpolationNode(), ActionDeploy, false, "", runID, nil)
	if err != nil {
		t.Fatalf("planHelm: %v", err)
	}

	values := req.Spec.Files[helm.ValuesFile]
	for _, want := range []string{
		"host: acme.example.com",           // vars.* (non-secret)
		"password: s3cret",                 // vars.* (sensitive — renders into the mounted Secret only)
		"tag: " + helm.GitSHAShortSentinel, // run.git_sha_short → sentinel, resolved in-script
		"full: " + helm.GitSHASentinel,
		"run: " + runID.String(),
		"ref: main", // run.git_ref = the configured git_ref, server-side
	} {
		if !strings.Contains(values, want) {
			t.Errorf("rendered values missing %q\n---\n%s", want, values)
		}
	}
	if strings.Contains(values, "${{") {
		t.Errorf("rendered values still carry references:\n%s", values)
	}

	// Namespace + release name render server-side into the script.
	for _, want := range []string{
		"helm upgrade --install 'web-acme'",
		"-n 'customer-acme'",
		"SF_SHA_SHORT=$(git -C /src rev-parse --short=7 HEAD)", // RenderGitContext sed
	} {
		if !strings.Contains(req.Spec.Script, want) {
			t.Errorf("script missing %q\n---\n%s", want, req.Spec.Script)
		}
	}

	// The TaskRun name prefix derives from the AUTHORED release name (rendered
	// values must never leak into persisted run names).
	if strings.Contains(req.Spec.Name, "acme") {
		t.Errorf("run prefix %q must not carry rendered variable values", req.Spec.Name)
	}
}

// TestPlanHelmInterpolationPreview: a preview renders the same fields — the
// diff must show what a deploy would deploy, sentinels included.
func TestPlanHelmInterpolationPreview(t *testing.T) {
	t.Parallel()
	app := &ent.Application{ID: uuid.New(), OrganizationID: uuid.New(), RunnerClusterID: uuid.New()}
	w := helmInterpolationWorker(map[string]string{"CUSTOMER_ID": "acme"}, map[string]string{"DB_PASSWORD": "x"})

	req, err := w.planHelm(context.Background(), app, helmInterpolationNode(), ActionPreview, false, "", uuid.New(), nil)
	if err != nil {
		t.Fatalf("planHelm(preview): %v", err)
	}
	if !strings.Contains(req.Spec.Files[helm.ValuesFile], "host: acme.example.com") {
		t.Errorf("preview values not rendered:\n%s", req.Spec.Files[helm.ValuesFile])
	}
	for _, want := range []string{
		"helm diff upgrade 'web-acme'",
		"-n 'customer-acme'",
		"SF_SHA_SHORT=", // the sed render applies to a preview too
	} {
		if !strings.Contains(req.Spec.Script, want) {
			t.Errorf("preview script missing %q\n---\n%s", want, req.Spec.Script)
		}
	}
}

// TestPlanHelmInterpolationFailures: a reference that cannot resolve fails the
// node at plan time (before any TaskRun is submitted) with the exact reference
// named — runComponent persists this as the component_run failure message.
func TestPlanHelmInterpolationFailures(t *testing.T) {
	t.Parallel()
	app := &ent.Application{ID: uuid.New(), OrganizationID: uuid.New(), RunnerClusterID: uuid.New()}
	w := helmInterpolationWorker(map[string]string{"CUSTOMER_ID": "Bad Value!"}, nil)

	// A variable that isn't defined names itself in the error.
	missing := helmInterpolationNode()
	missing.Config[helmConfigValues] = "a: ${{ vars.TYPO }}"
	_, err := w.planHelm(context.Background(), app, missing, ActionDeploy, false, "", uuid.New(), nil)
	if err == nil || !strings.Contains(err.Error(), `variable "TYPO" is not defined`) {
		t.Errorf("missing variable: got %v, want the variable named", err)
	}

	// A namespace that renders to an invalid Kubernetes name fails with the
	// rendered value named ("Bad Value!" is not a DNS-1123 label).
	badNS := helmInterpolationNode()
	badNS.Config[helmConfigValues] = ""
	_, err = w.planHelm(context.Background(), app, badNS, ActionDeploy, false, "", uuid.New(), nil)
	if err == nil || !strings.Contains(err.Error(), "customer-Bad Value!") {
		t.Errorf("invalid rendered namespace: got %v, want the rendered value named", err)
	}
}

// TestPlanHelmNoInterpolationUntouched: a node with no references plans exactly
// as before — values pass through byte-for-byte and no sed render is emitted.
func TestPlanHelmNoInterpolationUntouched(t *testing.T) {
	t.Parallel()
	app := &ent.Application{ID: uuid.New(), OrganizationID: uuid.New(), RunnerClusterID: uuid.New()}
	w := helmInterpolationWorker(nil, nil)
	target := uuid.New()
	node := GraphNode{
		ID: uuid.New(), ComponentID: uuid.New(), Name: "plain", Type: TypeHelm,
		TargetClusterID: &target,
		TargetNamespace: "apps",
		Config: map[string]string{
			helmConfigChartSource: helm.SourceGit,
			helm.ConfigRepoURL:    "https://github.com/org/charts.git",
			// Helm template braces and shell vars are not references and pass through.
			helmConfigValues: "name: {{ .Release.Name }}\nshell: $HOME\n",
		},
	}
	req, err := w.planHelm(context.Background(), app, node, ActionDeploy, false, "", uuid.New(), nil)
	if err != nil {
		t.Fatalf("planHelm: %v", err)
	}
	if got := req.Spec.Files[helm.ValuesFile]; got != node.Config[helmConfigValues] {
		t.Errorf("ref-free values must pass through unchanged, got:\n%s", got)
	}
	if strings.Contains(req.Spec.Script, "values.rendered.yaml") {
		t.Errorf("ref-free node must not emit the sed render:\n%s", req.Spec.Script)
	}
}

// helmOutputsWorker builds a worker whose resolveOutputs seam serves fixed
// outputs JSON per execution-unit id, for the components.* render tests.
func helmOutputsWorker(outputs map[uuid.UUID]string) *WorkflowRunWorker {
	return &WorkflowRunWorker{
		resolver: deploy.NewResolver(tokenConns{}, nil, nil, nil, plannerVars{}),
		resolveOutputs: func(_ context.Context, _, _, componentID uuid.UUID) (string, error) {
			return outputs[componentID], nil
		},
	}
}

// tofuSnapshotByID expands authored terraform components the same way a real
// run snapshot does (so the plan/apply units carry the production naming) and
// returns the nodes keyed by execution-unit id.
func tofuSnapshotByID(authored ...GraphNode) map[uuid.UUID]GraphNode {
	nodes := expandExecutionNodes(authored)
	byID := make(map[uuid.UUID]GraphNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	return byID
}

// tofuGraphNode builds a minimal authored terraform snapshot node.
func tofuGraphNode(id uuid.UUID, name string) GraphNode {
	return GraphNode{
		ID: id, ComponentID: id, Name: name, Type: TypeTerraform,
		Config: map[string]string{terraformConfigBackend: tofu.BackendS3},
	}
}

// helmOutputsRefNode builds a helm node whose values and namespace reference
// components.infra outputs.
func helmOutputsRefNode() GraphNode {
	target := uuid.New()
	return GraphNode{
		ID: uuid.New(), ComponentID: uuid.New(), Name: "web", Type: TypeHelm,
		TargetClusterID: &target,
		TargetNamespace: "${{ components.infra.outputs.namespace }}",
		Config: map[string]string{
			helmConfigChartSource: helm.SourceOCI,
			helm.ConfigRepoURL:    "oci://example.com/charts/app",
			helmConfigValues: "ns: ${{ components.infra.outputs.namespace }}\n" +
				"replicas: ${{ components.infra.outputs.replicas }}\n",
		},
	}
}

// TestPlanHelmComponentOutputs: ${{ components.<name>.outputs.<key> }} renders
// from the upstream apply unit's recorded outputs — string values bare,
// non-string values as compact JSON — in the inline values and the target
// namespace alike. The outputs row is keyed by the apply unit's derived id.
func TestPlanHelmComponentOutputs(t *testing.T) {
	t.Parallel()
	app := &ent.Application{ID: uuid.New(), OrganizationID: uuid.New(), RunnerClusterID: uuid.New()}
	infraID := uuid.New()
	byID := tofuSnapshotByID(tofuGraphNode(infraID, "infra"))
	outputs := `{
		"namespace": {"value": "team-a", "type": "string", "sensitive": false},
		"replicas":  {"value": [1, 2],   "type": ["list","number"], "sensitive": false}
	}`
	w := helmOutputsWorker(map[uuid.UUID]string{deriveApplyID(infraID): outputs})

	req, err := w.planHelm(context.Background(), app, helmOutputsRefNode(), ActionDeploy, false, "", uuid.New(), byID)
	if err != nil {
		t.Fatalf("planHelm: %v", err)
	}
	values := req.Spec.Files[helm.ValuesFile]
	for _, want := range []string{
		"ns: team-a",      // string output renders bare
		"replicas: [1,2]", // non-string output renders as compact JSON
	} {
		if !strings.Contains(values, want) {
			t.Errorf("rendered values missing %q\n---\n%s", want, values)
		}
	}
	// The namespace rendered server-side into the script.
	if !strings.Contains(req.Spec.Script, "-n 'team-a'") {
		t.Errorf("script missing the rendered namespace\n---\n%s", req.Spec.Script)
	}
}

// TestPlanHelmComponentOutputsFailures: an unresolvable components reference
// fails the node at plan time (before any TaskRun is submitted) with an
// actionable message naming the component — runComponent persists it as the
// component_run failure message.
func TestPlanHelmComponentOutputsFailures(t *testing.T) {
	t.Parallel()
	app := &ent.Application{ID: uuid.New(), OrganizationID: uuid.New(), RunnerClusterID: uuid.New()}
	infraID := uuid.New()
	byID := tofuSnapshotByID(tofuGraphNode(infraID, "infra"))

	// No recorded outputs anywhere (this run or earlier): deploy-first message.
	w := helmOutputsWorker(nil)
	_, err := w.planHelm(context.Background(), app, helmOutputsRefNode(), ActionDeploy, false, "", uuid.New(), byID)
	if err == nil || !strings.Contains(err.Error(), `component "infra" has no recorded outputs — deploy it successfully first`) {
		t.Errorf("no outputs: got %v, want the deploy-first message", err)
	}

	// Outputs exist but the key doesn't: the key is named.
	w = helmOutputsWorker(map[uuid.UUID]string{
		deriveApplyID(infraID): `{"other": {"value": "x", "type": "string", "sensitive": false}}`,
	})
	_, err = w.planHelm(context.Background(), app, helmOutputsRefNode(), ActionDeploy, false, "", uuid.New(), byID)
	if err == nil || !strings.Contains(err.Error(), `has no recorded output "namespace"`) {
		t.Errorf("missing key: got %v, want the key named", err)
	}

	// The named component isn't a terraform unit of this run (e.g. the snapshot
	// predates a rename).
	node := helmOutputsRefNode()
	node.Config[helmConfigValues] = "ns: ${{ components.bogus.outputs.namespace }}"
	node.TargetNamespace = "apps"
	_, err = w.planHelm(context.Background(), app, node, ActionDeploy, false, "", uuid.New(), byID)
	if err == nil || !strings.Contains(err.Error(), `component "bogus" is not an OpenTofu component of this run`) {
		t.Errorf("unknown component: got %v, want the component named", err)
	}

	// Two terraform components sharing the referenced name: ambiguous.
	dupByID := tofuSnapshotByID(tofuGraphNode(infraID, "infra"), tofuGraphNode(uuid.New(), "infra"))
	_, err = w.planHelm(context.Background(), app, helmOutputsRefNode(), ActionDeploy, false, "", uuid.New(), dupByID)
	if err == nil || !strings.Contains(err.Error(), `component name "infra" is ambiguous`) {
		t.Errorf("ambiguous name: got %v, want the ambiguity named", err)
	}
}

// TestPlanTofuHandoverFailure: a handover provision failure fails the planning
// (the step would only fail later, worse — at the kubectl upsert after a full
// plan), with the component named in the error.
func TestPlanTofuHandoverFailure(t *testing.T) {
	t.Parallel()
	runID, planID := uuid.New(), uuid.New()
	app := &ent.Application{ID: uuid.New(), OrganizationID: uuid.New(), RunnerClusterID: uuid.New()}
	w := &WorkflowRunWorker{
		resolver: deploy.NewResolver(plannerConns{}, nil, nil, nil, nil),
		ensureHandover: func(context.Context, k8s.Connection, string, string, map[string]string) error {
			return errors.New("forbidden")
		},
	}
	node := GraphNode{
		ID: planID, ComponentID: planID, Name: "net", Type: TypeTerraform,
		Config: map[string]string{
			terraformConfigCommand: terraformCommandPlan,
			terraformConfigBackend: tofu.BackendS3,
		},
	}
	_, err := w.planTofu(context.Background(), app, node, ActionDeploy, "", runID, map[uuid.UUID]GraphNode{planID: node})
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("expected the ensure failure to propagate, got %v", err)
	}
}
