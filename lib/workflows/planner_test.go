package workflows

import (
	"reflect"
	"testing"

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
