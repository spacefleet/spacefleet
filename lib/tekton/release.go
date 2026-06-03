// Package tekton turns a registered cluster's connection into Tekton-specific
// operations: detecting whether Tekton Pipelines is installed and healthy,
// installing it (server-side apply of a pinned, vendored release), and
// submitting/watching a minimal TaskRun. Like lib/k8s it takes a k8s.Connection
// and returns storage-agnostic structs — it leaks no client-go types to callers
// and never touches the database.
package tekton

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

// PinnedVersion is the Tekton Pipelines release vendored in release/release.yaml
// and applied by Install. It is the single source of truth for the version:
// bumping Tekton means replacing release/release.yaml and this constant
// together (release_test.go guards that the file parses). Detection compares a
// cluster's running version against this so the UI can later offer an upgrade.
const PinnedVersion = "v0.68.0"

// releaseYAML is the upstream Tekton Pipelines release manifest, vendored rather
// than fetched at runtime: installs are then reproducible and work in
// air-gapped/offline deployments, and a background job never depends on reaching
// the public release storage. See PinnedVersion to bump it.
//
//go:embed release/release.yaml
var releaseYAML []byte

// ReleaseObjects parses the vendored multi-document release manifest into
// unstructured objects, in file order (Install relies on the manifest's own
// ordering, then re-sorts CRDs/Namespaces to the front). Empty documents (the
// manifest's license-header separators) are skipped.
func ReleaseObjects() ([]*unstructured.Unstructured, error) {
	dec := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(releaseYAML), 4096)
	var objs []*unstructured.Unstructured
	for {
		var raw map[string]any
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("tekton: decode release manifest: %w", err)
		}
		if len(raw) == 0 {
			continue // empty document between license-header comments
		}
		obj := &unstructured.Unstructured{Object: raw}
		if obj.GetKind() == "" {
			continue
		}
		objs = append(objs, obj)
	}
	if len(objs) == 0 {
		return nil, fmt.Errorf("tekton: release manifest contained no objects")
	}
	return objs, nil
}
