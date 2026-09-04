package tekton

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestEnsurePluginCache: the claim is created RWX with the requested size and
// class (labelled as managed), a second ensure is a no-op that never touches
// the existing claim, an invalid size is rejected before any call, and delete
// removes it (and tolerates a missing claim).
func TestEnsurePluginCache(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset()
	ctx := context.Background()
	if err := ensurePluginCache(ctx, cs, "sf-jobs", "20Gi", "nfs"); err != nil {
		t.Fatalf("ensurePluginCache: %v", err)
	}
	pvc, err := cs.CoreV1().PersistentVolumeClaims("sf-jobs").Get(ctx, PluginCacheClaim, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get claim: %v", err)
	}
	if got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; got.String() != "20Gi" {
		t.Errorf("size = %s, want 20Gi", got.String())
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "nfs" {
		t.Errorf("storage class = %v, want nfs", pvc.Spec.StorageClassName)
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteMany {
		t.Errorf("access modes = %v, want ReadWriteMany", pvc.Spec.AccessModes)
	}
	if pvc.Labels[ManagedByLabel] != ManagedByValue {
		t.Errorf("labels = %v", pvc.Labels)
	}

	// Idempotent: a re-ensure with different settings leaves the claim alone.
	if err := ensurePluginCache(ctx, cs, "sf-jobs", "50Gi", ""); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	pvc, _ = cs.CoreV1().PersistentVolumeClaims("sf-jobs").Get(ctx, PluginCacheClaim, metav1.GetOptions{})
	if got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; got.String() != "20Gi" {
		t.Errorf("an existing claim must not be changed, size = %s", got.String())
	}

	if err := ensurePluginCache(ctx, cs, "sf-jobs", "lots", ""); err == nil {
		t.Error("invalid size must error")
	}
	for _, size := range []string{"", "0", "-1Gi", "abc"} {
		if ValidPluginCacheSize(size) {
			t.Errorf("ValidPluginCacheSize(%q) = true", size)
		}
	}
	if !ValidPluginCacheSize("20Gi") || !ValidPluginCacheSize("500M") {
		t.Error("valid sizes rejected")
	}

	if err := deletePluginCache(ctx, cs, "sf-jobs"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := cs.CoreV1().PersistentVolumeClaims("sf-jobs").Get(ctx, PluginCacheClaim, metav1.GetOptions{}); err == nil {
		t.Error("claim must be gone after delete")
	}
	if err := deletePluginCache(ctx, cs, "sf-jobs"); err != nil {
		t.Errorf("deleting a missing claim must be a no-op, got %v", err)
	}

	// No class: the cluster default (nil StorageClassName).
	if err := ensurePluginCache(ctx, cs, "sf-jobs", "1Gi", ""); err != nil {
		t.Fatalf("ensure default class: %v", err)
	}
	pvc, _ = cs.CoreV1().PersistentVolumeClaims("sf-jobs").Get(ctx, PluginCacheClaim, metav1.GetOptions{})
	if pvc.Spec.StorageClassName != nil {
		t.Errorf("empty class must leave StorageClassName nil, got %q", *pvc.Spec.StorageClassName)
	}
}
