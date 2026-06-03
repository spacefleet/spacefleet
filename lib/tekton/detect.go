package tekton

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/spacefleet/spacefleet/lib/k8s"
)

// Tekton's well-known identifiers. The controller runs in its own namespace and
// the API group is registered when the CRDs are applied.
const (
	// TektonGroup is the API group the Tekton CRDs register under.
	TektonGroup = "tekton.dev"
	// ControllerNamespace is where the upstream release installs the controller.
	ControllerNamespace = "tekton-pipelines"
	// ControllerDeployment is the controller Deployment's name; its readiness is
	// the signal that Tekton can actually reconcile runs.
	ControllerDeployment = "tekton-pipelines-controller"
	// versionLabel is the standard label the release stamps the controller with.
	versionLabel = "app.kubernetes.io/version"
)

// detectTimeout bounds detection so an unreachable API server can't hang the
// caller (mirrors k8s.probeTimeout).
const detectTimeout = 10 * time.Second

// Presence is the storage-agnostic result of detecting Tekton in a cluster:
// whether the API group is registered (Installed), whether the controller is
// ready to reconcile runs (ControllerReady), the controller's reported version,
// and a human-readable note when something is partial. It carries no client-go
// types so the API layer can map it like k8s.Node.
type Presence struct {
	// Installed reports that the Tekton API group is registered (CRDs applied).
	Installed bool
	// ControllerReady reports that the controller Deployment has a ready replica.
	ControllerReady bool
	// Managed reports that this Tekton was installed by Spacefleet (the controller
	// carries the app.kubernetes.io/managed-by=spacefleet label Install stamps).
	// Upgrade/delete are only offered when true — we never modify or remove a
	// Tekton the cluster owner installed themselves.
	Managed bool
	// Version is the controller's app.kubernetes.io/version label, if present.
	Version string
	// Message explains a partial state (e.g. CRDs present but controller absent).
	Message string
}

// Detect reports whether Tekton Pipelines is installed and healthy in the
// cluster the connection points at. It checks two independent signals: the
// Tekton API group's registration (via discovery) and the controller
// Deployment's readiness (via the typed client). A missing group or controller
// is "not installed" — not an error; only a failure to reach the API server is
// returned, so the handler can classify it like a node-list failure.
func Detect(ctx context.Context, conn k8s.Connection) (*Presence, error) {
	cfg, err := k8s.RESTConfig(ctx, conn)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = detectTimeout
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("tekton: client: %w", err)
	}
	return detect(ctx, cs)
}

// detect runs the detection against an already-built clientset. It is split out
// from Detect so tests can drive it with a fake clientset (the same pattern as
// k8s.inspect).
func detect(ctx context.Context, cs kubernetes.Interface) (*Presence, error) {
	p := &Presence{}

	groups, err := cs.Discovery().ServerGroups()
	if err != nil {
		return nil, fmt.Errorf("tekton: discovery: %w", err)
	}
	for _, g := range groups.Groups {
		if g.Name == TektonGroup {
			p.Installed = true
			break
		}
	}

	dep, err := cs.AppsV1().Deployments(ControllerNamespace).Get(ctx, ControllerDeployment, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		// No controller. If the group is present too this is a partial install
		// (CRDs applied, controller gone); otherwise Tekton simply isn't here.
		if p.Installed {
			p.Message = "Tekton CRDs are present but the controller is not installed"
		}
		return p, nil
	case err != nil:
		return nil, fmt.Errorf("tekton: get controller deployment: %w", err)
	}

	// A controller deployment exists, so Tekton is installed regardless of how
	// discovery's group cache looks.
	p.Installed = true
	p.ControllerReady = dep.Status.ReadyReplicas > 0
	p.Managed = dep.GetLabels()[ManagedByLabel] == ManagedByValue
	p.Version = deploymentVersion(dep.GetLabels(), dep.Spec.Template.GetLabels())
	if !p.ControllerReady {
		p.Message = "Tekton controller is installed but not ready"
	}
	return p, nil
}

// deploymentVersion reads the standard version label from the deployment, then
// its pod template, returning the first non-empty value.
func deploymentVersion(labels, templateLabels map[string]string) string {
	if v := labels[versionLabel]; v != "" {
		return v
	}
	return templateLabels[versionLabel]
}
