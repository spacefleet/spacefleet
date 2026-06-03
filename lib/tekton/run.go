package tekton

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	toolswatch "k8s.io/client-go/tools/watch"

	"github.com/spacefleet/spacefleet/lib/k8s"
)

// taskRunGVR is the GroupVersionResource for Tekton TaskRuns (tekton.dev/v1).
var taskRunGVR = schema.GroupVersionResource{Group: TektonGroup, Version: "v1", Resource: "taskruns"}

// RunSpec is the minimal description of a job to run: a single step that runs a
// script in an image. It is deliberately small — enough to prove the
// submit → watch → logs path end to end, not a full pipeline authoring surface.
type RunSpec struct {
	// Name is a prefix for the generated TaskRun name (a unique suffix is added).
	Name string
	// Image is the container image the step runs in.
	Image string
	// Script is the shell script the step executes.
	Script string
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
func SubmitTaskRun(ctx context.Context, conn k8s.Connection, namespace string, spec RunSpec) (*RunStatus, error) {
	ri, err := taskRuns(ctx, conn, namespace)
	if err != nil {
		return nil, err
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": TektonGroup + "/v1",
		"kind":       "TaskRun",
		"metadata": map[string]any{
			"generateName": spec.Name + "-",
			"namespace":    namespace,
		},
		"spec": map[string]any{
			"taskSpec": map[string]any{
				"steps": []any{
					map[string]any{
						"name":   "run",
						"image":  spec.Image,
						"script": spec.Script,
					},
				},
			},
		},
	}}
	created, err := ri.Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("tekton: create taskrun: %w", err)
	}
	return toRunStatus(created), nil
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
