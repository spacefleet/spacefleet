package tekton

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// controllerDeployment builds the Tekton controller deployment with the given
// ready-replica count and version label.
func controllerDeployment(ready int32, version string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ControllerDeployment,
			Namespace: ControllerNamespace,
			Labels:    map[string]string{versionLabel: version},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: ready},
	}
}

// tektonGroupResources is a discovery resource list that registers the
// tekton.dev/v1 API group, so ServerGroups reports it present.
func tektonGroupResources() []*metav1.APIResourceList {
	return []*metav1.APIResourceList{{
		GroupVersion: TektonGroup + "/v1",
		APIResources: []metav1.APIResource{{Name: "taskruns", Kind: "TaskRun"}, {Name: "pipelineruns", Kind: "PipelineRun"}},
	}}
}

func TestDetectNotInstalled(t *testing.T) {
	cs := fake.NewSimpleClientset()
	p, err := detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if p.Installed || p.ControllerReady {
		t.Errorf("expected not installed, got %+v", p)
	}
}

func TestDetectCRDsButNoController(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.Resources = tektonGroupResources()
	p, err := detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !p.Installed {
		t.Error("expected Installed (group present)")
	}
	if p.ControllerReady {
		t.Error("expected controller not ready")
	}
	if p.Message == "" {
		t.Error("expected a message explaining the partial install")
	}
}

func TestDetectReady(t *testing.T) {
	cs := fake.NewSimpleClientset(controllerDeployment(1, PinnedVersion))
	cs.Resources = tektonGroupResources()
	p, err := detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !p.Installed || !p.ControllerReady {
		t.Errorf("expected installed + ready, got %+v", p)
	}
	if p.Version != PinnedVersion {
		t.Errorf("Version = %q, want %q", p.Version, PinnedVersion)
	}
}

// TestDetectManaged reports Managed when the controller carries the ownership
// label Install stamps; an otherwise-identical unlabelled controller is not
// managed (it was installed outside Spacefleet).
func TestDetectManaged(t *testing.T) {
	dep := controllerDeployment(1, PinnedVersion)
	dep.Labels[ManagedByLabel] = ManagedByValue
	cs := fake.NewSimpleClientset(dep)
	cs.Resources = tektonGroupResources()
	p, err := detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !p.Managed {
		t.Error("expected Managed (ownership label present)")
	}
}

// TestDetectRevision reads the manifest-set revision the install stamped on
// the controller; a controller without the label (an install that predates
// revision stamping) reports an empty revision.
func TestDetectRevision(t *testing.T) {
	dep := controllerDeployment(1, PinnedVersion)
	dep.Labels[ManagedByLabel] = ManagedByValue
	dep.Labels[RevisionLabel] = "abc123def456"
	cs := fake.NewSimpleClientset(dep)
	cs.Resources = tektonGroupResources()
	p, err := detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if p.Revision != "abc123def456" {
		t.Errorf("Revision = %q, want %q", p.Revision, "abc123def456")
	}
}

func TestDetectRevisionAbsent(t *testing.T) {
	cs := fake.NewSimpleClientset(controllerDeployment(1, PinnedVersion))
	cs.Resources = tektonGroupResources()
	p, err := detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if p.Revision != "" {
		t.Errorf("Revision = %q, want empty for an unlabelled controller", p.Revision)
	}
}

func TestDetectUnmanaged(t *testing.T) {
	cs := fake.NewSimpleClientset(controllerDeployment(1, PinnedVersion))
	cs.Resources = tektonGroupResources()
	p, err := detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if p.Managed {
		t.Error("expected not managed (no ownership label)")
	}
}

// TestDetectControllerWithoutDiscovery covers a controller deployment present
// even when the discovery cache hasn't surfaced the group: the deployment branch
// still reports Installed.
func TestDetectControllerWithoutDiscovery(t *testing.T) {
	cs := fake.NewSimpleClientset(controllerDeployment(0, PinnedVersion))
	p, err := detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !p.Installed {
		t.Error("expected Installed via the controller deployment")
	}
	if p.ControllerReady {
		t.Error("expected not ready (0 replicas)")
	}
}
