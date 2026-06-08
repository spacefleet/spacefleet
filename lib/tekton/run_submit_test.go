package tekton

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"k8s.io/client-go/kubernetes/fake"
)

// TestBuildTaskRunWithoutFiles: a fileless spec has no volume/mount, and env is
// only present when supplied.
func TestBuildTaskRunWithoutFiles(t *testing.T) {
	tr := buildTaskRun("default", "", RunSpec{Name: "job", Image: "alpine", Script: "echo hi"})
	step := firstStep(t, tr)
	if _, ok := step["volumeMounts"]; ok {
		t.Error("fileless run should have no volumeMounts")
	}
	if _, found, _ := unstructured.NestedSlice(tr.Object, "spec", "taskSpec", "volumes"); found {
		t.Error("fileless run should have no volumes")
	}
	if _, ok := step["env"]; ok {
		t.Error("env should be absent when none supplied")
	}
	if gn, _, _ := unstructured.NestedString(tr.Object, "metadata", "generateName"); gn != "job-" {
		t.Errorf("generateName = %q, want job-", gn)
	}
}

// TestBuildTaskRunWithFiles: a spec with Files gets a read-only secret volume
// mounted at credsMountPath, plus sorted env entries.
func TestBuildTaskRunWithFiles(t *testing.T) {
	spec := RunSpec{
		Name:   "helm",
		Image:  "helm:1",
		Script: "helm upgrade",
		Env:    map[string]string{"B": "2", "A": "1"},
		Files:  map[string]string{"kubeconfig": "k", "values.yaml": "v"},
	}
	tr := buildTaskRun("ns", credsSecretName("abcd"), spec)
	step := firstStep(t, tr)

	mounts, ok := step["volumeMounts"].([]any)
	if !ok || len(mounts) != 1 {
		t.Fatalf("want 1 volumeMount, got %v", step["volumeMounts"])
	}
	m := mounts[0].(map[string]any)
	if m["mountPath"] != CredsMountPath || m["readOnly"] != true || m["name"] != credsVolumeName {
		t.Errorf("unexpected mount %v", m)
	}
	vols, _, _ := unstructured.NestedSlice(tr.Object, "spec", "taskSpec", "volumes")
	if len(vols) != 1 {
		t.Fatalf("want 1 volume, got %v", vols)
	}
	secret := vols[0].(map[string]any)["secret"].(map[string]any)
	if secret["secretName"] != "helm-creds-abcd" {
		t.Errorf("volume secretName = %v", secret["secretName"])
	}
	env := step["env"].([]any)
	if len(env) != 2 || env[0].(map[string]any)["name"] != "A" || env[1].(map[string]any)["name"] != "B" {
		t.Errorf("env not sorted/complete: %v", env)
	}
}

func TestBuildCredsSecret(t *testing.T) {
	s := buildCredsSecret("ns", "helm-creds-x", RunSpec{Files: map[string]string{"kubeconfig": "data"}})
	if s.Type != corev1.SecretTypeOpaque || s.Namespace != "ns" || s.Name != "helm-creds-x" {
		t.Errorf("unexpected secret meta %+v", s.ObjectMeta)
	}
	if s.StringData["kubeconfig"] != "data" {
		t.Errorf("StringData = %v", s.StringData)
	}
}

// TestBuildCredsSecretSecretEnv: sensitive env values are stored in the Secret
// under the env- prefixed key, alongside any mounted files.
func TestBuildCredsSecretSecretEnv(t *testing.T) {
	s := buildCredsSecret("ns", "helm-creds-x", RunSpec{
		Files:     map[string]string{"kubeconfig": "k"},
		SecretEnv: map[string]string{"API_KEY": "shh"},
	})
	if s.StringData["kubeconfig"] != "k" {
		t.Errorf("StringData[kubeconfig] = %q", s.StringData["kubeconfig"])
	}
	if s.StringData["env-API_KEY"] != "shh" {
		t.Errorf("StringData[env-API_KEY] = %q, want shh", s.StringData["env-API_KEY"])
	}
}

// TestBuildTaskRunSecretEnv: a sensitive env var is rendered as a secretKeyRef
// (never inline), a non-secret one stays inline, and with no Files there's no
// volume mount.
func TestBuildTaskRunSecretEnv(t *testing.T) {
	spec := RunSpec{
		Name:      "job",
		Image:     "img",
		Script:    "run",
		Env:       map[string]string{"PLAIN": "1"},
		SecretEnv: map[string]string{"SEC": "shh"},
	}
	tr := buildTaskRun("ns", credsSecretName("abcd"), spec)
	step := firstStep(t, tr)

	if _, ok := step["volumeMounts"]; ok {
		t.Error("no Files -> no volumeMounts even with SecretEnv")
	}
	env := step["env"].([]any)
	if len(env) != 2 {
		t.Fatalf("want 2 env entries, got %d: %v", len(env), env)
	}
	// Sorted by name: PLAIN before SEC.
	plain := env[0].(map[string]any)
	if plain["name"] != "PLAIN" || plain["value"] != "1" {
		t.Errorf("inline env = %v, want PLAIN=1", plain)
	}
	sec := env[1].(map[string]any)
	if sec["name"] != "SEC" {
		t.Errorf("secret env name = %v, want SEC", sec["name"])
	}
	if _, leaked := sec["value"]; leaked {
		t.Error("sensitive env rendered an inline value; it must use secretKeyRef")
	}
	ref := sec["valueFrom"].(map[string]any)["secretKeyRef"].(map[string]any)
	if ref["name"] != credsSecretName("abcd") || ref["key"] != "env-SEC" {
		t.Errorf("secretKeyRef = %v, want name=helm-creds-abcd key=env-SEC", ref)
	}
}

// TestSubmitTaskRunOrdering drives submitTaskRun with fake clients and asserts:
// the Secret is created before the TaskRun, and afterwards the Secret carries an
// ownerReference to the created TaskRun (so it's GC'd with the run).
func TestSubmitTaskRunOrdering(t *testing.T) {
	var order []string
	scheme := runtime.NewScheme()
	gvr := taskRunGVR
	listKinds := map[schema.GroupVersionResource]string{gvr: "TaskRunList"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)

	// The fake dynamic client doesn't honor generateName; assign a name+UID on
	// create so the ownerRef patch has something to reference.
	dyn.PrependReactor("create", "taskruns", func(a k8stesting.Action) (bool, runtime.Object, error) {
		obj := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		obj.SetName("helm-run-xyz")
		obj.SetUID(types.UID("run-uid-123"))
		order = append(order, "taskrun")
		return false, obj, nil
	})

	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		order = append(order, "secret")
		return false, nil, nil
	})

	ri := dyn.Resource(gvr).Namespace("default")
	st, err := submitTaskRun(context.Background(), ri, cs.CoreV1().Secrets("default"), "default", RunSpec{
		Name:   "helm",
		Image:  "helm:1",
		Script: "helm upgrade",
		Files:  map[string]string{"kubeconfig": "kube", "values.yaml": "vals"},
	})
	if err != nil {
		t.Fatalf("submitTaskRun: %v", err)
	}
	if st.Name != "helm-run-xyz" {
		t.Errorf("returned run name = %q", st.Name)
	}
	if len(order) != 2 || order[0] != "secret" || order[1] != "taskrun" {
		t.Fatalf("create order = %v, want [secret taskrun]", order)
	}

	sec, err := cs.CoreV1().Secrets("default").Get(context.Background(), credsSecretName(secretIDFromActions(t, cs)), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].UID != types.UID("run-uid-123") {
		t.Errorf("secret ownerReferences = %+v, want one ref to run-uid-123", sec.OwnerReferences)
	}
}

func firstStep(t *testing.T, tr *unstructured.Unstructured) map[string]any {
	t.Helper()
	steps, found, err := unstructured.NestedSlice(tr.Object, "spec", "taskSpec", "steps")
	if err != nil || !found || len(steps) != 1 {
		t.Fatalf("expected one step, got found=%v err=%v steps=%v", found, err, steps)
	}
	return steps[0].(map[string]any)
}

// secretIDFromActions recovers the random secret name created during the test by
// scanning the clientset's recorded create action.
func secretIDFromActions(t *testing.T, cs *fake.Clientset) string {
	t.Helper()
	for _, a := range cs.Actions() {
		if a.GetVerb() == "create" && a.GetResource().Resource == "secrets" {
			s := a.(k8stesting.CreateAction).GetObject().(*corev1.Secret)
			return s.Name[len("helm-creds-"):]
		}
	}
	t.Fatal("no secret create action recorded")
	return ""
}
