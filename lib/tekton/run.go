package tekton

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
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

// Per-run labels stamped on every TaskRun (and its creds Secret) at creation.
// RunOrgLabel carries the owning organization id so that two organizations
// registering the same physical runner cluster are scoped apart: a lookup can
// require a matching org-id label, so one org can't resolve another's run by
// name and read its status or pod logs. RunJobLabel carries the River job id so
// a worker can recover a run it already submitted (by listing on this label)
// when the persisted run name was lost to a crash between submit and the write.
const (
	RunOrgLabel = "spacefleet.io/org-id"
	RunJobLabel = "spacefleet.io/job-id"
	// RunComponentLabel ties a TaskRun to its ComponentRun within a workflow run.
	// A single workflow River job submits *many* TaskRuns under one job id, so the
	// job-id label alone is no longer unique per run — recovering the right
	// in-flight TaskRun after a crash needs a per-component key. The workflow
	// executor stamps this with the ComponentRun id and recovers on it (the
	// ComponentRun row is persisted before submit), while helm's single-run workers
	// keep recovering on the job-id label.
	RunComponentLabel = "spacefleet.io/component-run-id"
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
	// Env is the step's environment variables (name → value), rendered inline in
	// the TaskRun. For non-secret values only. Optional.
	Env map[string]string
	// SecretEnv is the step's sensitive environment variables (name → value).
	// Their values are stored in the per-run creds Secret and referenced from the
	// step via env.valueFrom.secretKeyRef, so they never appear inline in the
	// TaskRun manifest. Optional. When set (even without Files), a Secret is
	// created. Names must be disjoint from Env.
	SecretEnv map[string]string
	// Files are mounted into the step at CredsMountPath via a secret-backed
	// volume (filename → contents). Optional. Used to inject the target
	// kubeconfig + values.yaml into a Helm rollout without leaking multi-line
	// secrets through env. When non-empty, a Secret is created first (and
	// owner-referenced to the TaskRun for GC).
	Files map[string]string
	// Labels are stamped onto the TaskRun (and its creds Secret) at creation.
	// Optional. Callers use them for per-org scoping (RunOrgLabel) and
	// crash-safe re-attach (RunJobLabel); see ListRuns.
	Labels map[string]string
	// ServiceAccountName, when set, runs the TaskRun's pod as this
	// ServiceAccount instead of the namespace default. The terraform plan/apply
	// nodes set it to their pair's handover identity (see EnsureHandoverSecret)
	// so the step's in-pod kubectl can touch exactly its own planfile Secret —
	// the namespace default SA has (and needs) no permissions at all.
	ServiceAccountName string
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
	// A Secret is needed when the step has mounted Files or sensitive env vars
	// (whose values live in the Secret and are referenced via secretKeyRef).
	if len(spec.Files) == 0 && len(spec.SecretEnv) == 0 {
		created, err := taskruns.Create(ctx, buildTaskRun(namespace, "", spec), metav1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("tekton: create taskrun: %w", err)
		}
		return toRunStatus(created), nil
	}

	id := randID()
	secretName := credsSecretName(id)
	// Create the Secret first so the volume the TaskRun references already exists.
	// It carries the same per-org/per-job labels as the TaskRun so it is scoped
	// and swept alongside it.
	secret := buildCredsSecret(namespace, secretName, spec)
	if len(spec.Labels) > 0 {
		secret.Labels = spec.Labels
	}
	if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err != nil {
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
	// won't be auto-collected — but if no TaskRun pruner exists the Secret
	// lingers, so warn rather than swallow silently so it's diagnosable.
	if patch := ownerRefPatch(created.GetName(), created.GetUID()); patch != nil {
		if _, perr := secrets.Patch(ctx, secretName, types.MergePatchType, patch, metav1.PatchOptions{}); perr != nil {
			slog.WarnContext(ctx, "tekton: failed to set ownerReference on creds secret; it will not be auto-collected unless a TaskRun pruner sweeps it",
				"secret", secretName, "taskrun", created.GetName(), "error", perr)
		}
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
	if env := envVars(spec.Env, spec.SecretEnv, secretName); len(env) > 0 {
		step["env"] = env
	}
	taskSpec := map[string]any{"steps": []any{step}}
	generateName := spec.Name + "-"
	// A volume mount is only needed for mounted Files; secret-backed env uses
	// secretKeyRef and needs no mount.
	if len(spec.Files) > 0 {
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
	metadata := map[string]any{
		"generateName": generateName,
		"namespace":    namespace,
	}
	if len(spec.Labels) > 0 {
		metadata["labels"] = labelsToAny(spec.Labels)
	}
	trSpec := map[string]any{"taskSpec": taskSpec}
	if spec.ServiceAccountName != "" {
		trSpec["serviceAccountName"] = spec.ServiceAccountName
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": TektonGroup + "/v1",
		"kind":       "TaskRun",
		"metadata":   metadata,
		"spec":       trSpec,
	}}
}

// labelsToAny copies a label map into the map[string]any shape an unstructured
// object's metadata.labels expects.
func labelsToAny(labels map[string]string) map[string]any {
	out := make(map[string]any, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return out
}

// buildCredsSecret builds the Opaque Secret carrying the run's mounted Files
// (keyed by filename) and its sensitive env values (keyed by secretEnvDataKey),
// so the step can mount the files and reference the env values via secretKeyRef.
func buildCredsSecret(namespace, name string, spec RunSpec) *corev1.Secret {
	data := make(map[string]string, len(spec.Files)+len(spec.SecretEnv))
	for k, v := range spec.Files {
		data[k] = v
	}
	for k, v := range spec.SecretEnv {
		data[secretEnvDataKey(k)] = v
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeOpaque,
		StringData: data,
	}
}

// secretEnvDataKey is the creds-Secret data key holding a sensitive env value.
// The "env-" prefix namespaces it away from any mounted-file key (filenames
// don't carry it), and the result is a valid Secret data key.
func secretEnvDataKey(name string) string { return "env-" + name }

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

// envVars builds the TaskRun step's env list from the inline (non-secret) env
// and the secret env (whose values live in the creds Secret named secretName and
// are referenced via secretKeyRef). Entries are sorted by name across both maps
// so the rendered spec is stable (deterministic across submits and easy to
// assert); env and secretEnv names are expected to be disjoint.
func envVars(env, secretEnv map[string]string, secretName string) []any {
	names := make([]string, 0, len(env)+len(secretEnv))
	for k := range env {
		names = append(names, k)
	}
	for k := range secretEnv {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]any, 0, len(names))
	for _, k := range names {
		if _, ok := secretEnv[k]; ok {
			out = append(out, map[string]any{
				"name": k,
				"valueFrom": map[string]any{
					"secretKeyRef": map[string]any{
						"name": secretName,
						"key":  secretEnvDataKey(k),
					},
				},
			})
			continue
		}
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

// ErrRunForbidden is returned by the org-scoped lookups when a run exists by
// name but does not carry the caller's org-id label — i.e. it belongs to a
// different organization on a shared runner cluster. Callers map it to a 404 so
// the existence of another org's run isn't disclosed.
var ErrRunForbidden = fmt.Errorf("tekton: run not owned by this organization")

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

// GetRunScoped is GetRun with a per-org guard: it fetches the named run and
// returns ErrRunForbidden unless the run carries RunOrgLabel=orgLabel. An empty
// orgLabel disables the guard (behaves like GetRun) for callers that don't scope.
func GetRunScoped(ctx context.Context, conn k8s.Connection, namespace, name, orgLabel string) (*RunStatus, error) {
	ri, err := taskRuns(ctx, conn, namespace)
	if err != nil {
		return nil, err
	}
	u, err := ri.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("tekton: get taskrun: %w", err)
	}
	if !ownsRun(u, orgLabel) {
		return nil, ErrRunForbidden
	}
	return toRunStatus(u), nil
}

// ListRuns returns the status of every TaskRun in namespace matching the label
// selector (e.g. RunJobLabel=<jobID>). It is how a worker recovers a run it
// already submitted when the persisted run name was lost between submit and the
// write: the run carries the job-id label, so it can be found without a name.
func ListRuns(ctx context.Context, conn k8s.Connection, namespace, labelSelector string) ([]*RunStatus, error) {
	ri, err := taskRuns(ctx, conn, namespace)
	if err != nil {
		return nil, err
	}
	list, err := ri.List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil, fmt.Errorf("tekton: list taskruns: %w", err)
	}
	out := make([]*RunStatus, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, toRunStatus(&list.Items[i]))
	}
	return out, nil
}

// ownsRun reports whether u carries RunOrgLabel=orgLabel. An empty orgLabel
// matches anything (guard disabled).
func ownsRun(u *unstructured.Unstructured, orgLabel string) bool {
	if orgLabel == "" {
		return true
	}
	return u.GetLabels()[RunOrgLabel] == orgLabel
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
	return WatchRunScoped(ctx, conn, namespace, name, "")
}

// WatchRunScoped is WatchRun with a per-org guard: the initial Get returns
// ErrRunForbidden unless the run carries RunOrgLabel=orgLabel. An empty orgLabel
// disables the guard (behaves like WatchRun).
func WatchRunScoped(ctx context.Context, conn k8s.Connection, namespace, name, orgLabel string) (*RunStream, error) {
	ri, err := taskRuns(ctx, conn, namespace)
	if err != nil {
		return nil, err
	}
	u, err := ri.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("tekton: get taskrun: %w", err)
	}
	if !ownsRun(u, orgLabel) {
		return nil, ErrRunForbidden
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
			// Surface the init failure instead of closing silently: a bare close looks
			// like a clean end, so a caller that reconnects on close would spin
			// reconnecting immediately with no signal as to why. Log the error so the
			// failure is diagnosable before the channel closes.
			slog.ErrorContext(ctx, "tekton: failed to start run watcher; stream will close",
				"run", name, "namespace", namespace, "error", err)
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
