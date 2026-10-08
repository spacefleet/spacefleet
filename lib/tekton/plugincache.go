package tekton

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/spacefleet/spacefleet/lib/k8s"
)

// The OpenTofu provider plugin cache: one PersistentVolumeClaim per runner
// cluster, in the jobs namespace, mounted into every OpenTofu step at
// PluginCacheMountPath and exported as TF_PLUGIN_CACHE_DIR, so a provider is
// downloaded once per cluster rather than once per run — the biggest
// init-time win for a module with large providers. The claim asks for
// ReadWriteMany so steps scheduled on any node can share it; a StorageClass
// without RWX support leaves the claim (and every step) pending, which is why
// the class is an explicit setting.
const (
	// PluginCacheClaim is the claim's name in JobsNamespace.
	PluginCacheClaim = "spacefleet-tofu-plugin-cache"
	// PluginCacheMountPath is where the claim is mounted in a step.
	PluginCacheMountPath = "/plugins"
	// pluginCacheVolumeName is the TaskRun volume name for the claim.
	pluginCacheVolumeName = "plugin-cache"
)

// EnsurePluginCache creates the plugin-cache claim in namespace when it does
// not exist, requesting size (a Kubernetes quantity such as "20Gi") from
// storageClass (empty = the cluster default). An existing claim is left as it
// is: a PVC's size can only grow in place through the storage provider, and
// its class is immutable, so changing either means removing the cache first.
// The claim carries the managed-by labels so it is attributable and swept by
// Uninstall's labelled sweep never touches it (it is not in the release set).
func EnsurePluginCache(ctx context.Context, conn k8s.Connection, namespace, size, storageClass string) error {
	cfg, err := k8s.RESTConfig(ctx, conn)
	if err != nil {
		return err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("tekton: clientset: %w", err)
	}
	return ensurePluginCache(ctx, cs, namespace, size, storageClass)
}

// ensurePluginCache is the storage-agnostic core of EnsurePluginCache, taking
// the already-built clientset so tests can drive it with a fake.
func ensurePluginCache(ctx context.Context, cs kubernetes.Interface, namespace, size, storageClass string) error {
	qty, err := resource.ParseQuantity(size)
	if err != nil {
		return fmt.Errorf("tekton: plugin cache size %q: %w", size, err)
	}
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PluginCacheClaim,
			Namespace: namespace,
			Labels: map[string]string{
				ManagedByLabel: ManagedByValue,
				ComponentLabel: "tofu-plugin-cache",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: qty},
			},
		},
	}
	if storageClass != "" {
		sc := storageClass
		claim.Spec.StorageClassName = &sc
	}
	_, err = cs.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, claim, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("tekton: create plugin cache claim: %w", err)
	}
	// The claim exists: reconcile what can change in place. A claim's
	// storage class is immutable, so a different class is refused with a
	// pointer to the remove-and-recreate path (the cache is rebuildable, but
	// deleting a claim that running steps still mount would hang on the
	// pvc-protection finalizer, so that stays an explicit user action). The
	// size can grow through volume expansion when the class allows it — the
	// only field Kubernetes lets a bound claim change — and never shrink.
	existing, err := cs.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, PluginCacheClaim, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("tekton: read plugin cache claim: %w", err)
	}
	haveClass := ""
	if existing.Spec.StorageClassName != nil {
		haveClass = *existing.Spec.StorageClassName
	}
	if storageClass != "" && storageClass != haveClass {
		return fmt.Errorf("%w: the cache uses storage class %q; remove it and set it up again to use %q", ErrPluginCacheClassImmutable, haveClass, storageClass)
	}
	have := existing.Spec.Resources.Requests[corev1.ResourceStorage]
	switch qty.Cmp(have) {
	case 0:
		return nil
	case -1:
		return fmt.Errorf("%w: the cache is %s; a claim cannot shrink to %s — remove it and set it up again", ErrPluginCacheShrink, have.String(), qty.String())
	}
	if existing.Spec.Resources.Requests == nil {
		existing.Spec.Resources.Requests = corev1.ResourceList{}
	}
	existing.Spec.Resources.Requests[corev1.ResourceStorage] = qty
	if _, err := cs.CoreV1().PersistentVolumeClaims(namespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("tekton: grow plugin cache claim to %s (the storage class must allow volume expansion): %w", qty.String(), err)
	}
	return nil
}

// Errors an in-place plugin cache change can refuse with — user-facing,
// mapped to a 400 by the API. Both point at remove-and-recreate, the only
// way to change a claim's class or make it smaller.
var (
	ErrPluginCacheClassImmutable = errors.New("tekton: plugin cache storage class cannot change in place")
	ErrPluginCacheShrink         = errors.New("tekton: plugin cache cannot shrink")
)

// DeletePluginCache removes the plugin-cache claim from namespace; a missing
// claim is not an error. The cached providers go with it.
func DeletePluginCache(ctx context.Context, conn k8s.Connection, namespace string) error {
	cfg, err := k8s.RESTConfig(ctx, conn)
	if err != nil {
		return err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("tekton: clientset: %w", err)
	}
	return deletePluginCache(ctx, cs, namespace)
}

func deletePluginCache(ctx context.Context, cs kubernetes.Interface, namespace string) error {
	err := cs.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, PluginCacheClaim, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("tekton: delete plugin cache claim: %w", err)
	}
	return nil
}

// ValidPluginCacheSize reports whether size parses as a Kubernetes quantity
// with a positive value, for validating the setting before touching a cluster.
func ValidPluginCacheSize(size string) bool {
	qty, err := resource.ParseQuantity(size)
	return err == nil && qty.Sign() > 0
}
