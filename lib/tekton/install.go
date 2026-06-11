package tekton

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
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

// RevisionLabel records which manifest set (ManifestRevision) last applied an
// object. Detection reads it off the controller Deployment and compares it to
// this binary's ManifestRevision, so an install whose footprint changed —
// even at the same pinned Tekton version — reads as out of sync and the UI
// can offer an update. Installs that predate the label have no value, which
// deliberately also reads as out of sync.
const RevisionLabel = "spacefleet.io/revision"

// managedSelector selects every object Install stamped, for the uninstall sweep.
const managedSelector = ManagedByLabel + "=" + ManagedByValue

// JobsNamespace is the dedicated namespace on a runner cluster where every
// Spacefleet-submitted TaskRun lives, together with its per-run plumbing (creds
// Secrets, planfile-handover Secret + SA/Role/RoleBinding) — one place to look,
// kept out of the cluster's default namespace. Install creates it alongside the
// vendored release (and Uninstall removes it); a Tekton installed outside
// Spacefleet doesn't have it, so the cluster owner must create it themselves
// before runs can be submitted.
const JobsNamespace = "spacefleet-jobs"

// installTimeout bounds each apply/delete call. Installing is many small calls,
// not one long one, so a per-call cap is enough; the worker's context bounds the
// whole job.
const installTimeout = 2 * time.Minute

// Install applies the vendored, pinned Tekton release — plus the dedicated
// JobsNamespace runs are submitted to — to the cluster via server-side apply
// and returns PinnedVersion on success. It is idempotent:
// server-side apply converges, so a re-run (a second enable, or a River retry)
// is safe and doubles as the upgrade path. progress, if non-nil, is called with
// a short line per applied object so the caller can persist coarse progress.
//
// Installing Tekton creates CRDs, cluster-scoped RBAC, and webhooks — it needs
// cluster-admin-equivalent credentials. A credentials-too-weak failure surfaces
// here as a forbidden error for the caller to record.
func Install(ctx context.Context, conn k8s.Connection, progress func(string)) (string, error) {
	objs, err := installObjects()
	if err != nil {
		return "", err
	}
	apply, err := newApplier(ctx, conn)
	if err != nil {
		return "", err
	}
	rev := ManifestRevision()
	// Namespaces first (namespaced objects need them), then CRDs, then the rest.
	sortInstallOrder(objs)
	for _, obj := range objs {
		withManagedLabels(obj, rev)
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
	objs, err := installObjects()
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

// installObjects is the full object set Install applies (and Uninstall
// removes): the vendored, pinned release plus the dedicated jobs namespace.
// Appending the namespace here (rather than editing the vendored manifest)
// keeps the release byte-identical to upstream; sortInstallOrder still applies
// it first, the reverse-order uninstall deletes it last, and manifestNamespaces
// picks it up for the namespaced sweep.
func installObjects() ([]*unstructured.Unstructured, error) {
	objs, err := ReleaseObjects()
	if err != nil {
		return nil, err
	}
	return append(objs, jobsNamespaceObject()), nil
}

// jobsNamespaceObject builds the JobsNamespace Namespace. Ownership labels are
// stamped by the Install loop (withManagedLabels), like every release object.
func jobsNamespaceObject() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": JobsNamespace},
	}}
}

// withManagedLabels stamps the ownership and revision labels onto obj, merging
// into any labels the upstream release already set (Tekton's own
// app.kubernetes.io/* labels are preserved).
func withManagedLabels(obj *unstructured.Unstructured, revision string) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[ManagedByLabel] = ManagedByValue
	labels[ComponentLabel] = ComponentValue
	if revision != "" {
		labels[RevisionLabel] = revision
	}
	obj.SetLabels(labels)
}

// manifestRevision computes the fingerprint once: the install set is embedded,
// so it cannot change for the life of the process.
var manifestRevision = sync.OnceValue(func() string {
	objs, err := installObjects()
	if err != nil {
		// Unreachable for a binary whose embedded release parses (guarded by
		// release_test.go); Install surfaces the parse error itself.
		return ""
	}
	h := sha256.New()
	for _, obj := range objs {
		b, err := json.Marshal(obj.Object)
		if err != nil {
			return ""
		}
		h.Write(b)
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
})

// ManifestRevision is a short, deterministic fingerprint of the full object
// set Install applies — the vendored release plus the objects appended around
// it (the jobs namespace, …) — hashed before label stamping. Any change to
// that set, including one that doesn't bump PinnedVersion, yields a new
// revision; Install stamps it on every object (RevisionLabel) so detection can
// tell whether a cluster's install matches what this binary would apply.
func ManifestRevision() string { return manifestRevision() }

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
