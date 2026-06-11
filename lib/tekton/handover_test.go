package tekton

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"k8s.io/client-go/kubernetes/fake"
)

// TestEnsureHandoverSecret: a fresh ensure creates all four objects — the
// (empty) Secret, and a ServiceAccount/Role/RoleBinding owner-referenced to it
// — with the Role pinned by resourceNames to exactly the handover Secret and
// granting only the name-pinnable verbs (no create).
func TestEnsureHandoverSecret(t *testing.T) {
	cs := fake.NewSimpleClientset()
	labels := map[string]string{RunOrgLabel: "org-1", RunJobLabel: "run-1"}
	if err := ensureHandoverSecret(context.Background(), cs, "default", "tfplan-run1-plan1", labels); err != nil {
		t.Fatalf("ensureHandoverSecret: %v", err)
	}

	sec, err := cs.CoreV1().Secrets("default").Get(context.Background(), "tfplan-run1-plan1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if len(sec.Data) != 0 || len(sec.StringData) != 0 {
		t.Errorf("pre-created secret must be empty, got data=%v stringData=%v", sec.Data, sec.StringData)
	}
	if !reflect.DeepEqual(sec.Labels, labels) {
		t.Errorf("secret labels = %v, want %v", sec.Labels, labels)
	}

	sa, err := cs.CoreV1().ServiceAccounts("default").Get(context.Background(), "tfplan-run1-plan1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get serviceaccount: %v", err)
	}
	assertOwnedBySecret(t, "serviceaccount", sa.OwnerReferences, sec.UID)
	if !reflect.DeepEqual(sa.Labels, labels) {
		t.Errorf("serviceaccount labels = %v, want %v", sa.Labels, labels)
	}

	role, err := cs.RbacV1().Roles("default").Get(context.Background(), "tfplan-run1-plan1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get role: %v", err)
	}
	assertOwnedBySecret(t, "role", role.OwnerReferences, sec.UID)
	if len(role.Rules) != 1 {
		t.Fatalf("role rules = %+v, want exactly one", role.Rules)
	}
	rule := role.Rules[0]
	if !reflect.DeepEqual(rule.Resources, []string{"secrets"}) ||
		!reflect.DeepEqual(rule.ResourceNames, []string{"tfplan-run1-plan1"}) {
		t.Errorf("role rule not pinned to the handover secret: %+v", rule)
	}
	if !reflect.DeepEqual(rule.Verbs, []string{"get", "patch", "delete"}) {
		t.Errorf("role verbs = %v, want [get patch delete] (create must never be granted)", rule.Verbs)
	}

	rb, err := cs.RbacV1().RoleBindings("default").Get(context.Background(), "tfplan-run1-plan1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get rolebinding: %v", err)
	}
	assertOwnedBySecret(t, "rolebinding", rb.OwnerReferences, sec.UID)
	if rb.RoleRef.Kind != "Role" || rb.RoleRef.Name != "tfplan-run1-plan1" {
		t.Errorf("roleRef = %+v", rb.RoleRef)
	}
	if len(rb.Subjects) != 1 || rb.Subjects[0].Kind != "ServiceAccount" ||
		rb.Subjects[0].Name != "tfplan-run1-plan1" || rb.Subjects[0].Namespace != "default" {
		t.Errorf("subjects = %+v, want the same-named serviceaccount", rb.Subjects)
	}
}

// TestEnsureHandoverSecretIdempotent: re-ensuring against an existing Secret —
// one that already carries a stored planfile — must not touch its data, must
// still backfill any missing RBAC objects (a crash between creates), and must
// anchor their ownerReferences to the existing Secret's UID.
func TestEnsureHandoverSecretIdempotent(t *testing.T) {
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tfplan-run1-plan1",
			Namespace: "default",
			UID:       types.UID("secret-uid-1"),
		},
		Data: map[string][]byte{"tfplan": []byte("planfile-bytes")},
	}
	cs := fake.NewSimpleClientset(existing)

	if err := ensureHandoverSecret(context.Background(), cs, "default", "tfplan-run1-plan1", nil); err != nil {
		t.Fatalf("ensureHandoverSecret over existing secret: %v", err)
	}

	sec, err := cs.CoreV1().Secrets("default").Get(context.Background(), "tfplan-run1-plan1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if string(sec.Data["tfplan"]) != "planfile-bytes" {
		t.Errorf("stored planfile was clobbered: %v", sec.Data)
	}
	role, err := cs.RbacV1().Roles("default").Get(context.Background(), "tfplan-run1-plan1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get role: %v", err)
	}
	assertOwnedBySecret(t, "role", role.OwnerReferences, types.UID("secret-uid-1"))

	// A full second pass over the now-complete set is also a no-op.
	if err := ensureHandoverSecret(context.Background(), cs, "default", "tfplan-run1-plan1", nil); err != nil {
		t.Fatalf("second ensureHandoverSecret: %v", err)
	}
}

// TestDeleteHandoverSecret: deleting removes the Secret, and a Secret that is
// already gone (the apply step's own cleanup) is a no-op rather than an error.
func TestDeleteHandoverSecret(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tfplan-run1-plan1", Namespace: "default"},
	})
	if err := deleteHandoverSecret(context.Background(), cs, "default", "tfplan-run1-plan1"); err != nil {
		t.Fatalf("deleteHandoverSecret: %v", err)
	}
	if _, err := cs.CoreV1().Secrets("default").Get(context.Background(), "tfplan-run1-plan1", metav1.GetOptions{}); err == nil {
		t.Error("secret still present after delete")
	}
	if err := deleteHandoverSecret(context.Background(), cs, "default", "tfplan-run1-plan1"); err != nil {
		t.Errorf("delete of an already-gone secret should be a no-op, got %v", err)
	}
}

// assertOwnedBySecret asserts the object carries exactly one ownerReference,
// pointing at the handover Secret, so deleting the Secret GCs it.
func assertOwnedBySecret(t *testing.T, kind string, refs []metav1.OwnerReference, uid types.UID) {
	t.Helper()
	if len(refs) != 1 {
		t.Fatalf("%s ownerReferences = %+v, want exactly one", kind, refs)
	}
	ref := refs[0]
	if ref.Kind != "Secret" || ref.APIVersion != "v1" || ref.UID != uid {
		t.Errorf("%s ownerReference = %+v, want the handover secret (uid %s)", kind, ref, uid)
	}
}
