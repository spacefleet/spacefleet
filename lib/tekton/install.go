package tekton

import (
	"context"
	"fmt"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"

	"github.com/spacefleet/spacefleet/lib/k8s"
)

// fieldManager identifies Spacefleet as the owner of the fields it applies, so
// server-side apply can converge on re-runs without fighting other managers.
const fieldManager = "spacefleet"

// Ownership labels stamped on every object Install applies. ManagedByLabel is
// the standard Kubernetes recommended label; we set it to ManagedByValue so a
// Tekton this app installed can be told apart from one the cluster owner
// installed themselves — detection reads it (see Detect), the API gates
// upgrade/delete on it, and Uninstall sweeps by it. ComponentLabel scopes the
// ownership to the Tekton install specifically.
const (
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedByValue = "spacefleet"
	ComponentLabel = "spacefleet.io/component"
	ComponentValue = "tekton"
)

// managedSelector selects every object Install stamped, for the uninstall sweep.
const managedSelector = ManagedByLabel + "=" + ManagedByValue

// installTimeout bounds each apply/delete call. Installing is many small calls,
// not one long one, so a per-call cap is enough; the worker's context bounds the
// whole job.
const installTimeout = 2 * time.Minute

// Install applies the vendored, pinned Tekton release to the cluster via
// server-side apply and returns PinnedVersion on success. It is idempotent:
// server-side apply converges, so a re-run (a second enable, or a River retry)
// is safe and doubles as the upgrade path. progress, if non-nil, is called with
// a short line per applied object so the caller can persist coarse progress.
//
// Installing Tekton creates CRDs, cluster-scoped RBAC, and webhooks — it needs
// cluster-admin-equivalent credentials. A credentials-too-weak failure surfaces
// here as a forbidden error for the caller to record.
func Install(ctx context.Context, conn k8s.Connection, progress func(string)) (string, error) {
	objs, err := ReleaseObjects()
	if err != nil {
		return "", err
	}
	apply, err := newApplier(ctx, conn)
	if err != nil {
		return "", err
	}
	// Namespaces first (namespaced objects need them), then CRDs, then the rest.
	sortInstallOrder(objs)
	for _, obj := range objs {
		withManagedLabels(obj)
		if err := apply.apply(ctx, obj); err != nil {
			return "", err
		}
		if progress != nil {
			progress(fmt.Sprintf("applied %s/%s", obj.GetKind(), obj.GetName()))
		}
	}
	return PinnedVersion, nil
}

// Uninstall removes the release objects in reverse install order, best-effort
// (a NotFound is ignored — it's already gone). It is the hook the uninstalling
// lifecycle state needs; it does not attempt to drain in-flight runs.
func Uninstall(ctx context.Context, conn k8s.Connection, progress func(string)) error {
	objs, err := ReleaseObjects()
	if err != nil {
		return err
	}
	apply, err := newApplier(ctx, conn)
	if err != nil {
		return err
	}
	sortInstallOrder(objs)
	for i := len(objs) - 1; i >= 0; i-- {
		obj := objs[i]
		if err := apply.delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if progress != nil {
			progress(fmt.Sprintf("removed %s/%s", obj.GetKind(), obj.GetName()))
		}
	}
	// The ordered delete above removes exactly what the pinned manifest lists.
	// Sweep by ownership label to also catch stragglers the list missed — a
	// resource a newer Tekton added that our pinned manifest doesn't name, or a
	// leftover from a partial install. Bounded to the resource types we install
	// (so we never touch unrelated objects) and to objects we labelled.
	if err := apply.sweep(ctx, objs, progress); err != nil {
		return err
	}
	return nil
}

// withManagedLabels stamps the ownership labels onto obj, merging into any
// labels the upstream release already set (Tekton's own app.kubernetes.io/*
// labels are preserved).
func withManagedLabels(obj *unstructured.Unstructured) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[ManagedByLabel] = ManagedByValue
	labels[ComponentLabel] = ComponentValue
	obj.SetLabels(labels)
}

// applier resolves each object's GroupVersionKind to a REST resource and applies
// it with the right (namespaced vs cluster) scope.
type applier struct {
	dyn    dynamic.Interface
	mapper *restmapper.DeferredDiscoveryRESTMapper
}

func newApplier(ctx context.Context, conn k8s.Connection) (*applier, error) {
	cfg, err := k8s.RESTConfig(ctx, conn)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = installTimeout
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("tekton: dynamic client: %w", err)
	}
	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("tekton: discovery client: %w", err)
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(disc))
	return &applier{dyn: dyn, mapper: mapper}, nil
}

func (a *applier) resource(obj *unstructured.Unstructured) (dynamic.ResourceInterface, error) {
	gvk := obj.GroupVersionKind()
	m, err := a.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		// A freshly-applied CRD may not be in the cache yet; reset and retry once.
		a.mapper.Reset()
		m, err = a.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return nil, fmt.Errorf("tekton: map %s: %w", gvk, err)
		}
	}
	if m.Scope.Name() == meta.RESTScopeNameNamespace {
		ns := obj.GetNamespace()
		if ns == "" {
			ns = ControllerNamespace
		}
		return a.dyn.Resource(m.Resource).Namespace(ns), nil
	}
	return a.dyn.Resource(m.Resource), nil
}

func (a *applier) apply(ctx context.Context, obj *unstructured.Unstructured) error {
	ri, err := a.resource(obj)
	if err != nil {
		return err
	}
	force := true
	_, err = ri.Apply(ctx, obj.GetName(), obj, metav1.ApplyOptions{FieldManager: fieldManager, Force: force})
	if err != nil {
		return fmt.Errorf("tekton: apply %s/%s: %w", obj.GetKind(), obj.GetName(), err)
	}
	return nil
}

func (a *applier) delete(ctx context.Context, obj *unstructured.Unstructured) error {
	ri, err := a.resource(obj)
	if err != nil {
		return err
	}
	return ri.Delete(ctx, obj.GetName(), metav1.DeleteOptions{})
}

// sweep deletes any remaining objects carrying the ownership label, across the
// distinct resource types the release installs (cluster-scoped types swept
// cluster-wide, namespaced types across the manifest's namespaces). It is the
// safety net behind the ordered delete: it catches objects we labelled that the
// current pinned manifest no longer lists (version drift) without ever touching
// resource types Tekton doesn't use. A resource type whose CRD is already gone
// is skipped (nothing left to sweep).
func (a *applier) sweep(ctx context.Context, objs []*unstructured.Unstructured, progress func(string)) error {
	namespaces := manifestNamespaces(objs)
	seen := map[schema.GroupVersionResource]bool{}
	for _, obj := range objs {
		gvk := obj.GroupVersionKind()
		m, err := a.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			a.mapper.Reset()
			if m, err = a.mapper.RESTMapping(gvk.GroupKind(), gvk.Version); err != nil {
				continue // type already removed (its CRD is gone) — nothing to sweep
			}
		}
		if seen[m.Resource] {
			continue
		}
		seen[m.Resource] = true
		if m.Scope.Name() == meta.RESTScopeNameNamespace {
			for _, ns := range namespaces {
				if err := a.sweepResource(ctx, a.dyn.Resource(m.Resource).Namespace(ns), progress); err != nil {
					return err
				}
			}
			continue
		}
		if err := a.sweepResource(ctx, a.dyn.Resource(m.Resource), progress); err != nil {
			return err
		}
	}
	return nil
}

func (a *applier) sweepResource(ctx context.Context, ri dynamic.ResourceInterface, progress func(string)) error {
	list, err := ri.List(ctx, metav1.ListOptions{LabelSelector: managedSelector})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil // the namespace or resource type is already gone
		}
		return err
	}
	for i := range list.Items {
		item := &list.Items[i]
		if err := ri.Delete(ctx, item.GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if progress != nil {
			progress(fmt.Sprintf("swept %s/%s", item.GetKind(), item.GetName()))
		}
	}
	return nil
}

// manifestNamespaces is the set of namespaces the release defines (plus the
// controller namespace), used to scope the namespaced sweep.
func manifestNamespaces(objs []*unstructured.Unstructured) []string {
	set := map[string]bool{ControllerNamespace: true}
	for _, obj := range objs {
		if obj.GetKind() == "Namespace" {
			set[obj.GetName()] = true
		}
	}
	out := make([]string, 0, len(set))
	for ns := range set {
		out = append(out, ns)
	}
	return out
}

// sortInstallOrder stably reorders objects so Namespaces apply first (namespaced
// objects depend on them) and CustomResourceDefinitions next, with everything
// else after. The vendored release contains no tekton.dev custom resources, so
// no CRD-establishment wait is needed beyond this ordering.
func sortInstallOrder(objs []*unstructured.Unstructured) {
	rank := func(kind string) int {
		switch kind {
		case "Namespace":
			return 0
		case "CustomResourceDefinition":
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(objs, func(i, j int) bool {
		return rank(objs[i].GetKind()) < rank(objs[j].GetKind())
	})
}
