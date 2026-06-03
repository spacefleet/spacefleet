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
