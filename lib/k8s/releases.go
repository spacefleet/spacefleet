package k8s

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// Release is a storage-agnostic view of a Helm release discovered on a cluster:
// the subset the import flow needs to pre-fill an Application. Like Namespace it
// is flattened into plain Go types so the API layer can map it without importing
// client-go or the Helm SDK. Values is the release's user-supplied config
// rendered to YAML (what `helm get values` shows), the pre-fill for an
// application's inline values.
type Release struct {
	Name         string
	Namespace    string
	ChartName    string
	ChartVersion string
	AppVersion   string
	Status       string
	Revision     int
	Values       string
	Updated      time.Time
}

// helmReleaseSecretLabel selects the Secrets Helm 3 writes for release state
// (owner=helm), across all revisions. The newest revision per (namespace, name)
// is kept by ListReleases.
const helmReleaseSecretLabel = "owner=helm"

// helmReleaseDataKey is the Secret data key under which Helm stores the encoded
// release object (base64 of gzip of JSON).
const helmReleaseDataKey = "release"

// helmRelease is the minimal shape of a decoded Helm 3 release object — only the
// fields the import flow surfaces. Helm stores far more (manifest, hooks, the
// full chart); we deliberately decode just this so we never depend on the Helm
// SDK's types.
type helmRelease struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Version   int    `json:"version"`
	Info      struct {
		Status       string `json:"status"`
		LastDeployed string `json:"last_deployed"`
	} `json:"info"`
	Chart struct {
		Metadata struct {
			Name       string `json:"name"`
			Version    string `json:"version"`
			AppVersion string `json:"appVersion"`
		} `json:"metadata"`
	} `json:"chart"`
	// Config is the user-supplied values (the overrides passed at install/upgrade),
	// not the chart defaults — the same thing `helm get values` returns.
	Config map[string]any `json:"config"`
}

// ListReleases builds a client from the connection and returns the Helm releases
// installed on the cluster, decoded from the release Secrets Helm 3 writes
// (storage driver `secret`, the default). When namespace is empty it lists across
// all namespaces; otherwise it scopes to that namespace. Only the newest revision
// of each release is returned. Like ListNamespaces it owns the timeout so a slow
// API server is bounded.
func ListReleases(ctx context.Context, conn Connection, namespace string) ([]Release, error) {
	cfg, err := RESTConfig(ctx, conn)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = listTimeout
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: client: %w", err)
	}
	return listReleases(ctx, cs, namespace)
}

// listReleases is the core of ListReleases over an already-built clientset. It is
// split out from ListReleases so tests can drive it with a fake clientset.
func listReleases(ctx context.Context, cs kubernetes.Interface, namespace string) ([]Release, error) {
	// An empty namespace lists release Secrets across every namespace, which
	// requires cluster-wide secrets:list (a ClusterRole binding); a single
	// namespace only needs secrets:list within that namespace.
	list, err := cs.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{LabelSelector: helmReleaseSecretLabel})
	if err != nil {
		return nil, fmt.Errorf("k8s: list helm releases: %w", err)
	}

	// Keep the newest revision per (namespace, name). A release has one Secret per
	// revision; the highest version is the current state.
	latest := map[string]Release{}
	skipped := 0
	for i := range list.Items {
		sec := &list.Items[i]
		raw, ok := sec.Data[helmReleaseDataKey]
		if !ok {
			skipped++
			continue
		}
		hr, err := decodeRelease(raw)
		if err != nil {
			// Skip an unreadable release rather than failing the whole listing — a
			// single malformed secret shouldn't hide every other release.
			skipped++
			continue
		}
		rel := Release{
			Name:         hr.Name,
			Namespace:    hr.Namespace,
			ChartName:    hr.Chart.Metadata.Name,
			ChartVersion: hr.Chart.Metadata.Version,
			AppVersion:   hr.Chart.Metadata.AppVersion,
			Status:       hr.Info.Status,
			Revision:     hr.Version,
			Values:       valuesYAML(hr.Config),
			Updated:      releaseUpdated(hr.Info.LastDeployed, sec.CreationTimestamp.Time),
		}
		key := rel.Namespace + "/" + rel.Name
		if prev, ok := latest[key]; !ok || rel.Revision > prev.Revision {
			latest[key] = rel
		}
	}

	// A skip is normal for some non-Helm-owned Secrets, but if a Helm storage
	// format change made every Secret undecodable this would otherwise present as
	// "no releases" with no signal — surface the count so it's diagnosable.
	if skipped > 0 {
		slog.DebugContext(ctx, "k8s: skipped undecodable helm release secrets",
			"skipped", skipped, "total", len(list.Items), "kept", len(latest))
	}

	out := make([]Release, 0, len(latest))
	for _, rel := range latest {
		out = append(out, rel)
	}
	return out, nil
}

// maxDecodedReleaseSize caps how far a single release payload may decompress.
// Release Secrets live on tenant-controlled clusters, so a crafted Secret could
// otherwise gzip-bomb the serve process: gzip expands up to ~1000x, and the
// ~768KiB of gzip that fits a 1MiB Secret after base64 would balloon to
// ~750MiB. Legitimate releases sit far below this — at typical ~5-15x ratios
// on JSON, the Secret size limit keeps real payloads under ~10MiB decoded.
const maxDecodedReleaseSize = 32 << 20 // 32 MiB

// decodeRelease reverses Helm 3's release encoding: the Secret data value is a
// base64 string of the gzip of the release JSON. (client-go has already decoded
// the Secret's own transport base64, so raw is the base64-of-gzip bytes.)
// Payloads that decompress past maxDecodedReleaseSize are rejected.
func decodeRelease(raw []byte) (*helmRelease, error) {
	gz, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		return nil, fmt.Errorf("base64: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = zr.Close() }()
	// Read one byte past the cap so an exactly-at-the-limit payload is
	// distinguishable from an oversized one.
	jsonBytes, err := io.ReadAll(io.LimitReader(zr, maxDecodedReleaseSize+1))
	if err != nil {
		return nil, fmt.Errorf("gunzip: %w", err)
	}
	if len(jsonBytes) > maxDecodedReleaseSize {
		return nil, fmt.Errorf("gunzip: decoded release exceeds %d bytes", maxDecodedReleaseSize)
	}
	var hr helmRelease
	if err := json.Unmarshal(jsonBytes, &hr); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	return &hr, nil
}

// valuesYAML renders a release's user-supplied config to a values.yaml-style
// string (the import pre-fill). An empty/nil config yields "" so the form starts
// blank rather than with "{}\n".
func valuesYAML(config map[string]any) string {
	if len(config) == 0 {
		return ""
	}
	b, err := yaml.Marshal(config)
	if err != nil {
		return ""
	}
	return string(b)
}

// releaseUpdated parses Helm's last_deployed timestamp, falling back to the
// release Secret's creation time (the revision Secret is created when the
// revision is deployed) when it is absent or unparseable.
func releaseUpdated(lastDeployed string, fallback time.Time) time.Time {
	if lastDeployed != "" {
		if t, err := time.Parse(time.RFC3339, lastDeployed); err == nil {
			return t
		}
	}
	return fallback
}
