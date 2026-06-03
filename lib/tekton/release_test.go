package tekton

import "testing"

// TestReleaseObjectsParses guards the vendored manifest: it must parse into many
// objects, each with a kind and name, and contain the controller deployment, the
// namespace, and the CRDs. A corrupt or truncated release.yaml fails here rather
// than at install time against a live cluster.
func TestReleaseObjectsParses(t *testing.T) {
	objs, err := ReleaseObjects()
	if err != nil {
		t.Fatalf("ReleaseObjects: %v", err)
	}
	if len(objs) < 20 {
		t.Fatalf("expected many objects, got %d", len(objs))
	}

	var crds int
	var sawController, sawNamespace bool
	for _, o := range objs {
		if o.GetKind() == "" {
			t.Errorf("object with empty kind: %v", o.Object)
		}
		if o.GetName() == "" {
			t.Errorf("%s with empty name", o.GetKind())
		}
		switch o.GetKind() {
		case "CustomResourceDefinition":
			crds++
		case "Deployment":
			if o.GetName() == ControllerDeployment {
				sawController = true
			}
		case "Namespace":
			if o.GetName() == ControllerNamespace {
				sawNamespace = true
			}
		}
	}
	if crds == 0 {
		t.Error("expected at least one CustomResourceDefinition")
	}
	if !sawController {
		t.Errorf("expected the %s deployment", ControllerDeployment)
	}
	if !sawNamespace {
		t.Errorf("expected the %s namespace", ControllerNamespace)
	}
}

// TestSortInstallOrder verifies Namespaces sort before CRDs before everything
// else, so namespaced objects land after their namespace exists.
func TestSortInstallOrder(t *testing.T) {
	objs, err := ReleaseObjects()
	if err != nil {
		t.Fatalf("ReleaseObjects: %v", err)
	}
	sortInstallOrder(objs)

	rank := map[string]int{"Namespace": 0, "CustomResourceDefinition": 1}
	prev := -1
	for _, o := range objs {
		r, ok := rank[o.GetKind()]
		if !ok {
			r = 2
		}
		if r < prev {
			t.Fatalf("object %s/%s (rank %d) sorted after rank %d", o.GetKind(), o.GetName(), r, prev)
		}
		prev = r
	}
}
