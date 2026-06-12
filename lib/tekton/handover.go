package tekton

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/spacefleet/spacefleet/lib/k8s"
)

// This file holds the planfile-handover plumbing for a terraform plan/apply
// pair: a Secret the plan step stores its saved planfile into and the apply
// step reads it back from, plus the per-pair ServiceAccount/Role/RoleBinding
// that scope both steps' pods to exactly that one Secret.
//
// The worker pre-creates the (empty) Secret rather than letting the plan step
// create it, for two reasons:
//
//   - Least privilege. `create` is the one RBAC verb that cannot be restricted
//     by resourceNames, so a step that may create Secrets can create (and thus
//     pre-plant) any Secret name in the namespace. With the Secret already
//     present, the plan step's `kubectl apply` upsert is a get+patch and the
//     apply step needs get+delete — all name-pinnable — so the Role grants the
//     user-supplied module code running in those pods access to nothing but its
//     own planfile. In particular it cannot read other runs' creds Secrets or
//     overwrite another run's pending planfile.
//   - Lifecycle. The Secret is the natural owner for the RBAC objects: they are
//     owner-referenced to it, so deleting the Secret (the apply step's cleanup,
//     or the worker's terminal sweep) garbage-collects all three with it.

// handoverVerbs are the secret verbs the pair's Role grants, pinned to the one
// handover Secret: get+patch for the plan step's client-side-apply upsert,
// get+delete for the apply step's restore and cleanup. Deliberately no create —
// see the file comment.
var handoverVerbs = []string{"get", "patch", "delete"}

// EnsureHandoverSecret ensures the named planfile-handover Secret exists in
// namespace, together with the same-named ServiceAccount, Role, and RoleBinding
// that let pods running as that ServiceAccount get/patch/delete exactly that
// Secret. It is idempotent: every object is create-if-missing, and an existing
// Secret's data is never touched (a retry or an approval-resume must not wipe a
// stored planfile). The RBAC objects are owner-referenced to the Secret so they
// are garbage-collected when it is deleted. labels are stamped on all four
// objects so operators (and a future janitor) can attribute and sweep them.
func EnsureHandoverSecret(ctx context.Context, conn k8s.Connection, namespace, name string, labels map[string]string) error {
	cfg, err := k8s.RESTConfig(ctx, conn)
	if err != nil {
		return err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("tekton: clientset: %w", err)
	}
	return ensureHandoverSecret(ctx, cs, namespace, name, labels)
}

// ensureHandoverSecret is the storage-agnostic core of EnsureHandoverSecret,
// taking the already-built clientset so tests can drive it with a fake.
func ensureHandoverSecret(ctx context.Context, cs kubernetes.Interface, namespace, name string, labels map[string]string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Type:       corev1.SecretTypeOpaque,
	}
	existing, err := cs.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		// A prior attempt already created it — and the plan step may since have
		// stored the planfile in it, so reuse it untouched. The Get is still
		// needed: the UID below anchors the RBAC objects' ownerReferences.
		existing, err = cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return fmt.Errorf("tekton: ensure handover secret %s/%s: %w", namespace, name, err)
	}

	meta := metav1.ObjectMeta{
		Name:      name,
		Namespace: namespace,
		Labels:    labels,
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "v1",
			Kind:       "Secret",
			Name:       name,
			UID:        existing.UID,
		}},
	}

	sa := &corev1.ServiceAccount{ObjectMeta: meta}
	if _, err := cs.CoreV1().ServiceAccounts(namespace).Create(ctx, sa, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("tekton: ensure handover serviceaccount %s/%s: %w", namespace, name, err)
	}

	// The Role's verbs are all name-pinnable, so a subject creating it only
	// needs to hold the same secret verbs itself (the API server's escalation
	// check) — no escalate/bind grant is required of the cluster credentials.
	role := &rbacv1.Role{
		ObjectMeta: meta,
		Rules: []rbacv1.PolicyRule{{
			APIGroups:     []string{""},
			Resources:     []string{"secrets"},
			ResourceNames: []string{name},
			Verbs:         handoverVerbs,
		}},
	}
	if _, err := cs.RbacV1().Roles(namespace).Create(ctx, role, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("tekton: ensure handover role %s/%s: %w", namespace, name, err)
	}

	binding := &rbacv1.RoleBinding{
		ObjectMeta: meta,
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: namespace}},
	}
	if _, err := cs.RbacV1().RoleBindings(namespace).Create(ctx, binding, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("tekton: ensure handover rolebinding %s/%s: %w", namespace, name, err)
	}
	return nil
}

// ReadHandoverSecretKey returns the named key's bytes from a handover Secret —
// the worker-side read of data a step handed back through it (a terraform
// apply's captured outputs). A missing Secret or key returns nil with no error:
// both are normal states (the capture is best-effort in the pod), distinct
// from a connection/API failure, which is returned for the caller to log.
func ReadHandoverSecretKey(ctx context.Context, conn k8s.Connection, namespace, name, key string) ([]byte, error) {
	cfg, err := k8s.RESTConfig(ctx, conn)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("tekton: clientset: %w", err)
	}
	return readHandoverSecretKey(ctx, cs, namespace, name, key)
}

// readHandoverSecretKey is the storage-agnostic core of ReadHandoverSecretKey.
func readHandoverSecretKey(ctx context.Context, cs kubernetes.Interface, namespace, name, key string) ([]byte, error) {
	secret, err := cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("tekton: read handover secret %s/%s: %w", namespace, name, err)
	}
	return secret.Data[key], nil
}

// DeleteHandoverSecret deletes the named handover Secret; the owner-referenced
// ServiceAccount/Role/RoleBinding are garbage-collected with it. A Secret that
// is already gone (the apply step's own cleanup got there first) is a no-op.
func DeleteHandoverSecret(ctx context.Context, conn k8s.Connection, namespace, name string) error {
	cfg, err := k8s.RESTConfig(ctx, conn)
	if err != nil {
		return err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("tekton: clientset: %w", err)
	}
	return deleteHandoverSecret(ctx, cs, namespace, name)
}

// deleteHandoverSecret is the storage-agnostic core of DeleteHandoverSecret.
func deleteHandoverSecret(ctx context.Context, cs kubernetes.Interface, namespace, name string) error {
	err := cs.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("tekton: delete handover secret %s/%s: %w", namespace, name, err)
	}
	return nil
}
