package tekton

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	toolswatch "k8s.io/client-go/tools/watch"

	"github.com/spacefleet/spacefleet/lib/k8s"
)

// taskRunGVR is the GroupVersionResource for Tekton TaskRuns (tekton.dev/v1).
var taskRunGVR = schema.GroupVersionResource{Group: TektonGroup, Version: "v1", Resource: "taskruns"}

// CredsMountPath is where a run's injected Files are mounted in the step (as a
// secret-backed volume). The Helm rollout reads its kubeconfig/values from here;
// it is exported so callers can build absolute paths to the files they inject.
const CredsMountPath = "/workspace/creds"

// credsVolumeName is the volume name shared by the secret volume and its mount.
const credsVolumeName = "creds"

// RunSpec is the minimal description of a job to run: a single step that runs a
// script in an image. It is deliberately small — enough to drive the
// submit → watch → logs path end to end, not a full pipeline authoring surface.
type RunSpec struct {
	// Name is a prefix for the generated TaskRun name (a unique suffix is added).
	Name string
	// Image is the container image the step runs in.
	Image string
	// Script is the shell script the step executes.
	Script string
	// Env is the step's environment variables (name → value). Optional.
	Env map[string]string
	// Files are mounted into the step at CredsMountPath via a secret-backed
	// volume (filename → contents). Optional. Used to inject the target
	// kubeconfig + values.yaml into a Helm rollout without leaking multi-line
	// secrets through env. When non-empty, a Secret is created first (and
	// owner-referenced to the TaskRun for GC).
	Files map[string]string
}

// RunStatus is the storage-agnostic view of a TaskRun's state. Phase is derived
// from the run's "Succeeded" condition: Pending (not started), Running,
// Succeeded, or Failed. It carries no client-go types.
type RunStatus struct {
	Name        string
	Namespace   string
	Phase       string
	PodName     string
	Message     string
	StartedAt   *time.Time
	CompletedAt *time.Time
}

// Terminal reports whether the run has finished (its pod won't change further),
// so a watcher can close the stream.
func (r RunStatus) Terminal() bool {
	return r.Phase == "Succeeded" || r.Phase == "Failed"
}

// SubmitTaskRun creates a single-step TaskRun (with an inline taskSpec, so no
// separate Task object is needed) and returns its initial status. The TaskRun
// uses generateName, so the returned RunStatus.Name is the server-assigned name.
//
// When spec.Files is non-empty the step also receives a secret-backed volume at
// CredsMountPath: a Secret is created first, then the TaskRun, then the Secret's
// ownerReferences are patched to the TaskRun so it is garbage-collected with the
// run. The cluster's REST config is built once and shared by the dynamic client
// (TaskRun) and the typed clientset (Secret) so a cloud token is minted once.
func SubmitTaskRun(ctx context.Context, conn k8s.Connection, namespace string, spec RunSpec) (*RunStatus, error) {
	cfg, err := k8s.RESTConfig(ctx, conn)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("tekton: dynamic client: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("tekton: clientset: %w", err)
	}
	return submitTaskRun(ctx, dyn.Resource(taskRunGVR).Namespace(namespace), cs.CoreV1().Secrets(namespace), namespace, spec)
}

// submitTaskRun is the storage-agnostic core of SubmitTaskRun, taking the
// already-built clients so tests can drive it with fakes (the same seam as
// detect). When spec carries Files it creates the creds Secret, then the
// TaskRun, then patches the Secret's ownerReferences with the TaskRun UID.
func submitTaskRun(ctx context.Context, taskruns dynamic.ResourceInterface, secrets typedcorev1.SecretInterface, namespace string, spec RunSpec) (*RunStatus, error) {
	if len(spec.Files) == 0 {
		created, err := taskruns.Create(ctx, buildTaskRun(namespace, "", spec), metav1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("tekton: create taskrun: %w", err)
		}
		return toRunStatus(created), nil
	}

	id := randID()
	secretName := credsSecretName(id)
	// Create the Secret first so the volume the TaskRun references already exists.
	if _, err := secrets.Create(ctx, buildCredsSecret(namespace, secretName, spec.Files), metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("tekton: create creds secret: %w", err)
	}
	created, err := taskruns.Create(ctx, buildTaskRun(namespace, secretName, spec), metav1.CreateOptions{})
	if err != nil {
		// Best-effort cleanup of the orphaned Secret (no TaskRun owns it yet).
		_ = secrets.Delete(ctx, secretName, metav1.DeleteOptions{})
		return nil, fmt.Errorf("tekton: create taskrun: %w", err)
	}
	// Now that the TaskRun has a UID, own the Secret to it so it's GC'd with the
	// run. A failure here is non-fatal: the run still works, the Secret just
	// won't be auto-collected (the operator's TaskRun pruner sweeps it).
	if patch := ownerRefPatch(created.GetName(), created.GetUID()); patch != nil {
		_, _ = secrets.Patch(ctx, secretName, types.MergePatchType, patch, metav1.PatchOptions{})
	}
	return toRunStatus(created), nil
}

// credsSecretName is the per-run Secret name holding the injected Files.
func credsSecretName(id string) string { return "helm-creds-" + id }

// buildTaskRun builds the single-step TaskRun (inline taskSpec) for spec. When
// secretName is non-empty the step gets a read-only secret-backed volume at
// CredsMountPath; the generateName carries the same id so run and Secret share
// a recognizable prefix.
func buildTaskRun(namespace, secretName string, spec RunSpec) *unstructured.Unstructured {
	step := map[string]any{
		"name":   "run",
		"image":  spec.Image,
		"script": spec.Script,
	}
	if len(spec.Env) > 0 {
		step["env"] = envVars(spec.Env)
	}
	taskSpec := map[string]any{"steps": []any{step}}
	generateName := spec.Name + "-"
	if secretName != "" {
		step["volumeMounts"] = []any{map[string]any{
			"name":      credsVolumeName,
			"mountPath": CredsMountPath,
			"readOnly":  true,
		}}
		taskSpec["volumes"] = []any{map[string]any{
			"name":   credsVolumeName,
			"secret": map[string]any{"secretName": secretName},
		}}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": TektonGroup + "/v1",
		"kind":       "TaskRun",
		"metadata": map[string]any{
			"generateName": generateName,
			"namespace":    namespace,
		},
		"spec": map[string]any{"taskSpec": taskSpec},
	}}
}

// buildCredsSecret builds the Opaque Secret carrying the run's Files (keyed by
// filename), mounted into the step as a volume.
func buildCredsSecret(namespace, name string, files map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeOpaque,
		StringData: files,
	}
}

// ownerRefPatch is a JSON merge patch that sets a Secret's single ownerReference
// to the named TaskRun, or nil if the UID isn't known (so we skip the patch).
func ownerRefPatch(taskRunName string, uid types.UID) []byte {
	if uid == "" {
		return nil
	}
	return []byte(fmt.Sprintf(
		`{"metadata":{"ownerReferences":[{"apiVersion":%q,"kind":"TaskRun","name":%q,"uid":%q,"blockOwnerDeletion":false}]}}`,
		TektonGroup+"/v1", taskRunName, string(uid),
	))
}

// envVars maps an env map to the TaskRun step's env list, sorted by name so the
// rendered spec is stable (deterministic across submits and easy to assert).
func envVars(env map[string]string) []any {
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]any, 0, len(env))
	for _, k := range names {
		out = append(out, map[string]any{"name": k, "value": env[k]})
	}
	return out
}

// randID returns a short random hex id for naming a run's Secret/volume.
func randID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// GetRun fetches one TaskRun's current status.
func GetRun(ctx context.Context, conn k8s.Connection, namespace, name string) (*RunStatus, error) {
	ri, err := taskRuns(ctx, conn, namespace)
	if err != nil {
		return nil, err
	}
	u, err := ri.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("tekton: get taskrun: %w", err)
	}
	return toRunStatus(u), nil
}

// RunStream is an open watch on a single TaskRun: the initial Snapshot plus a
// channel of subsequent status changes (as EventModified). The channel closes
// when the watch ends (context cancelled or unrecoverable error). Drain it from
// one goroutine; cancel the context passed to WatchRun to stop.
type RunStream struct {
	Snapshot RunStatus
	Events   <-chan k8s.Event[RunStatus]
}

// WatchRun returns the run's current status (the snapshot) then streams status
// changes until ctx is cancelled. The initial Get doubles as a reachability and
// existence check (returned synchronously); errors once live simply end the
// stream. Resilience comes from a RetryWatcher scoped to the single object by
// name.
func WatchRun(ctx context.Context, conn k8s.Connection, namespace, name string) (*RunStream, error) {
	ri, err := taskRuns(ctx, conn, namespace)
	if err != nil {
		return nil, err
	}
	u, err := ri.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("tekton: get taskrun: %w", err)
	}
	initial := toRunStatus(u)

	events := make(chan k8s.Event[RunStatus])
	go func() {
		defer close(events)
		lw := &cache.ListWatch{
			WatchFuncWithContext: func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
				opts.FieldSelector = "metadata.name=" + name
				return ri.Watch(ctx, opts)
			},
		}
		rw, err := toolswatch.NewRetryWatcherWithContext(ctx, u.GetResourceVersion(), lw)
		if err != nil {
			return
		}
		defer rw.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-rw.ResultChan():
				if !ok {
					return
				}
				if ev.Type != watch.Added && ev.Type != watch.Modified {
					continue
				}
				obj, ok := ev.Object.(*unstructured.Unstructured)
				if !ok {
					continue
				}
				select {
				case events <- k8s.Event[RunStatus]{Type: k8s.EventModified, Object: *toRunStatus(obj)}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return &RunStream{Snapshot: *initial, Events: events}, nil
}

// taskRuns builds a namespaced dynamic client for TaskRuns from the connection.
func taskRuns(ctx context.Context, conn k8s.Connection, namespace string) (dynamic.ResourceInterface, error) {
	cfg, err := k8s.RESTConfig(ctx, conn)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("tekton: dynamic client: %w", err)
	}
	return dyn.Resource(taskRunGVR).Namespace(namespace), nil
}

// toRunStatus maps a TaskRun's unstructured form to the storage-agnostic
// RunStatus, deriving Phase from the "Succeeded" condition.
func toRunStatus(u *unstructured.Unstructured) *RunStatus {
	r := &RunStatus{Name: u.GetName(), Namespace: u.GetNamespace(), Phase: "Pending"}
	r.PodName, _, _ = unstructured.NestedString(u.Object, "status", "podName")
	if t := parseStatusTime(u, "startTime"); t != nil {
		r.StartedAt = t
	}
	if t := parseStatusTime(u, "completionTime"); t != nil {
		r.CompletedAt = t
	}
	conds, found, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	if !found {
		return r
	}
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok || m["type"] != "Succeeded" {
			continue
		}
		status, _ := m["status"].(string)
		r.Message, _ = m["message"].(string)
		switch status {
		case "True":
			r.Phase = "Succeeded"
		case "False":
			r.Phase = "Failed"
		default:
			r.Phase = "Running"
		}
		break
	}
	return r
}

// parseStatusTime reads an RFC3339 timestamp from status.<field>, or nil.
func parseStatusTime(u *unstructured.Unstructured, field string) *time.Time {
	s, found, _ := unstructured.NestedString(u.Object, "status", field)
	if !found || s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}
