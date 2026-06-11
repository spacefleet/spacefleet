package tekton

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestWithManagedLabelsMerges stamps the ownership labels while preserving the
// upstream Tekton labels already on the object.
func TestWithManagedLabelsMerges(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{}}
	obj.SetLabels(map[string]string{"app.kubernetes.io/part-of": "tekton-pipelines"})

	withManagedLabels(obj)

	labels := obj.GetLabels()
	if labels[ManagedByLabel] != ManagedByValue {
		t.Errorf("%s = %q, want %q", ManagedByLabel, labels[ManagedByLabel], ManagedByValue)
	}
	if labels[ComponentLabel] != ComponentValue {
		t.Errorf("%s = %q, want %q", ComponentLabel, labels[ComponentLabel], ComponentValue)
	}
	if labels["app.kubernetes.io/part-of"] != "tekton-pipelines" {
		t.Error("withManagedLabels dropped an upstream label")
	}
}

// TestWithManagedLabelsNoExisting handles an object with no labels at all.
func TestWithManagedLabelsNoExisting(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{}}

	withManagedLabels(obj)

	if got := obj.GetLabels()[ManagedByLabel]; got != ManagedByValue {
		t.Errorf("%s = %q, want %q", ManagedByLabel, got, ManagedByValue)
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
