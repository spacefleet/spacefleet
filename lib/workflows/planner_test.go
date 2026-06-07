package workflows

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/manifest"
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
	// Explicit release_name wins.
	n := GraphNode{Name: "My App", Config: map[string]string{helmConfigReleaseName: "explicit-release"}}
	if got := componentReleaseName(n); got != "explicit-release" {
		t.Fatalf("release name with explicit: got %q", got)
	}
	if got := componentRunPrefix(n); got != "helm-explicit-release" {
		t.Fatalf("prefix with explicit: got %q", got)
	}
	// Falls back to the (sanitized) component name.
	n2 := GraphNode{Name: "My App", Config: map[string]string{}}
	if got := componentReleaseName(n2); got != "My App" {
		t.Fatalf("release name fallback: got %q", got)
	}
	if got := componentRunPrefix(n2); got != "helm-my-app" {
		t.Fatalf("prefix fallback: got %q", got)
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

func TestTofuSecretSuffixSharedByPair(t *testing.T) {
	t.Parallel()
	app := &ent.Application{ID: uuid.New()}
	planID := uuid.New()
	// A plan node keys state off its own id; its apply node keys off the SAME
	// (its upstream plan's) id — so the pair shares one backend state Secret, the
	// precondition for `tofu apply tfplan` to apply the plan node's saved plan.
	planSuffix := tofuSecretSuffix(app, planID)
	applySuffix := tofuSecretSuffix(app, planID)
	if planSuffix != applySuffix {
		t.Fatalf("plan and apply must share a state suffix: %q vs %q", planSuffix, applySuffix)
	}
	// A different deployment (different plan id) does not share state.
	if other := tofuSecretSuffix(app, uuid.New()); other == planSuffix {
		t.Fatalf("distinct plan ids must not collide: %q", other)
	}
	// The result is a DNS-1123 label (lowercase, hyphen-separated).
	if planSuffix == "" || planSuffix != sanitizeLabel(planSuffix) {
		t.Fatalf("suffix is not a sanitized label: %q", planSuffix)
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
