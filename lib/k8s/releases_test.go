package k8s

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// encodeRelease builds the Secret data value Helm 3 writes for a release:
// base64(gzip(json)). It mirrors decodeRelease in reverse.
func encodeRelease(t *testing.T, r map[string]any) []byte {
	t.Helper()
	j, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(j); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return []byte(base64.StdEncoding.EncodeToString(buf.Bytes()))
}

// releaseSecret builds a Helm release Secret for the given release name/revision.
func releaseSecret(t *testing.T, namespace, name string, revision int, status string, config map[string]any) corev1.Secret {
	t.Helper()
	rel := map[string]any{
		"name":      name,
		"namespace": namespace,
		"version":   revision,
		"info":      map[string]any{"status": status},
		"chart": map[string]any{
			"metadata": map[string]any{"name": "redis", "version": "1.2.3", "appVersion": "7.0"},
		},
		"config": config,
	}
	return corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      "sh.helm.release.v1." + name + ".v" + itoa(revision),
			Labels:    map[string]string{"owner": "helm", "name": name},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{"release": encodeRelease(t, rel)},
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestListReleasesDecodesLatestRevision(t *testing.T) {
	// Two revisions of the same release plus a second release. The newest revision
	// (v2, with the current values) must win.
	cs := fake.NewSimpleClientset(
		secretPtr(releaseSecret(t, "prod", "cache", 1, "superseded", map[string]any{"replicas": 1})),
		secretPtr(releaseSecret(t, "prod", "cache", 2, "deployed", map[string]any{"replicas": 3})),
		secretPtr(releaseSecret(t, "default", "web", 1, "deployed", nil)),
	)

	releases, err := listReleases(context.Background(), cs, "")
	if err != nil {
		t.Fatalf("listReleases: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("got %d releases, want 2 (one per name, newest revision)", len(releases))
	}

	byName := map[string]Release{}
	for _, r := range releases {
		byName[r.Name] = r
	}

	cache, ok := byName["cache"]
	if !ok {
		t.Fatal("missing release cache")
	}
	if cache.Revision != 2 {
		t.Errorf("cache revision = %d, want 2 (newest)", cache.Revision)
	}
	if cache.Status != "deployed" {
		t.Errorf("cache status = %q, want deployed", cache.Status)
	}
	if cache.ChartName != "redis" || cache.ChartVersion != "1.2.3" || cache.AppVersion != "7.0" {
		t.Errorf("cache chart = %q:%q (app %q), want redis:1.2.3 (app 7.0)", cache.ChartName, cache.ChartVersion, cache.AppVersion)
	}
	// The newest revision's user values, rendered to YAML.
	if cache.Values != "replicas: 3\n" {
		t.Errorf("cache values = %q, want %q", cache.Values, "replicas: 3\n")
	}

	web := byName["web"]
	if web.Values != "" {
		t.Errorf("web values = %q, want empty (no config)", web.Values)
	}
}

func TestListReleasesScopesToNamespace(t *testing.T) {
	cs := fake.NewSimpleClientset(
		secretPtr(releaseSecret(t, "prod", "cache", 1, "deployed", nil)),
		secretPtr(releaseSecret(t, "default", "web", 1, "deployed", nil)),
	)
	releases, err := listReleases(context.Background(), cs, "prod")
	if err != nil {
		t.Fatalf("listReleases: %v", err)
	}
	if len(releases) != 1 || releases[0].Name != "cache" {
		t.Fatalf("namespace scope returned %+v, want only cache in prod", releases)
	}
}

func secretPtr(s corev1.Secret) *corev1.Secret { return &s }
