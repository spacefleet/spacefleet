package tekton

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestWithManagedLabelsMerges stamps the ownership and revision labels while
// preserving the upstream Tekton labels already on the object.
func TestWithManagedLabelsMerges(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{}}
	obj.SetLabels(map[string]string{"app.kubernetes.io/part-of": "tekton-pipelines"})

	withManagedLabels(obj, "abc123def456")

	labels := obj.GetLabels()
	if labels[ManagedByLabel] != ManagedByValue {
		t.Errorf("%s = %q, want %q", ManagedByLabel, labels[ManagedByLabel], ManagedByValue)
	}
	if labels[ComponentLabel] != ComponentValue {
		t.Errorf("%s = %q, want %q", ComponentLabel, labels[ComponentLabel], ComponentValue)
	}
	if labels[RevisionLabel] != "abc123def456" {
		t.Errorf("%s = %q, want %q", RevisionLabel, labels[RevisionLabel], "abc123def456")
	}
	if labels["app.kubernetes.io/part-of"] != "tekton-pipelines" {
		t.Error("withManagedLabels dropped an upstream label")
	}
}

// TestWithManagedLabelsNoExisting handles an object with no labels at all, and
// an empty revision (the impossible parse-failure fallback) stamps no
// revision label rather than an empty value.
func TestWithManagedLabelsNoExisting(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{}}

	withManagedLabels(obj, "")

	if got := obj.GetLabels()[ManagedByLabel]; got != ManagedByValue {
		t.Errorf("%s = %q, want %q", ManagedByLabel, got, ManagedByValue)
	}
	if _, ok := obj.GetLabels()[RevisionLabel]; ok {
		t.Error("withManagedLabels stamped an empty revision label")
	}
}

// TestManifestRevision: the fingerprint of the embedded install set is
// non-empty, stable across calls, short enough for a label value, and — being
// derived from the objects themselves — is what makes a footprint change at
// the same PinnedVersion detectable as out-of-sync.
func TestManifestRevision(t *testing.T) {
	rev := ManifestRevision()
	if rev == "" {
		t.Fatal("ManifestRevision is empty — the embedded install set failed to parse or marshal")
	}
	if len(rev) != 12 {
		t.Errorf("ManifestRevision length = %d, want 12", len(rev))
	}
	if again := ManifestRevision(); again != rev {
		t.Errorf("ManifestRevision not stable: %q then %q", rev, again)
	}
}

// TestManifestNamespacesIncludesController collects the release's namespaces for
// the uninstall sweep, always including the controller namespace.
func TestManifestNamespacesIncludesController(t *testing.T) {
	objs, err := ReleaseObjects()
	if err != nil {
		t.Fatalf("ReleaseObjects: %v", err)
	}
	var found bool
	for _, ns := range manifestNamespaces(objs) {
		if ns == ControllerNamespace {
			found = true
		}
	}
	if !found {
		t.Errorf("manifestNamespaces missing %q", ControllerNamespace)
	}
}

// TestInstallObjectsIncludeJobsNamespace: the install set is the vendored
// release plus the dedicated jobs namespace, and the uninstall sweep's
// namespace list covers it — so runs submitted to JobsNamespace have somewhere
// to land on a managed cluster, and uninstall cleans it (and anything labelled
// inside it) back up.
func TestInstallObjectsIncludeJobsNamespace(t *testing.T) {
	release, err := ReleaseObjects()
	if err != nil {
		t.Fatalf("ReleaseObjects: %v", err)
	}
	objs, err := installObjects()
	if err != nil {
		t.Fatalf("installObjects: %v", err)
	}
	if len(objs) != len(release)+1 {
		t.Errorf("installObjects returned %d objects, want release (%d) + 1", len(objs), len(release))
	}
	var ns *unstructured.Unstructured
	for _, obj := range objs {
		if obj.GetKind() == "Namespace" && obj.GetName() == JobsNamespace {
			ns = obj
		}
	}
	if ns == nil {
		t.Fatalf("installObjects has no Namespace named %q", JobsNamespace)
	}
	var swept bool
	for _, name := range manifestNamespaces(objs) {
		if name == JobsNamespace {
			swept = true
		}
	}
	if !swept {
		t.Errorf("manifestNamespaces missing %q", JobsNamespace)
	}
}
