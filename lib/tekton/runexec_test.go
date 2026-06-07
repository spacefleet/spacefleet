package tekton

import (
	"context"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/spacefleet/spacefleet/lib/k8s"
)

// notFoundErr builds a Kubernetes NotFound status error the way the API client
// does, so apierrors.IsNotFound recognizes it (through GetRun's %w wrap too).
func notFoundErr(name string) error {
	return apierrors.NewNotFound(schema.GroupResource{Group: TektonGroup, Resource: "taskruns"}, name)
}

// TestExecuteTransientGetErrorDoesNotResubmit covers F6: when the candidate run
// name is set but Get fails transiently (not a NotFound), Execute must NOT submit
// a fresh run — that would risk a duplicate TaskRun for a run that may still be in
// flight. It returns the transient error so the job retries and re-attaches.
func TestExecuteTransientGetErrorDoesNotResubmit(t *testing.T) {
	transient := fmt.Errorf("tekton: get taskrun: %w", fmt.Errorf("connection refused"))
	submitted := 0
	f := RunFuncs{
		Get: func(context.Context, k8s.Connection, string, string) (*RunStatus, error) {
			return nil, transient
		},
		Submit: func(context.Context, k8s.Connection, string, RunSpec) (*RunStatus, error) {
			submitted++
			return &RunStatus{Name: "run-new"}, nil
		},
	}
	name, final, err := f.Execute(context.Background(), RunRequest{ExistingRun: "run-old"})
	if err == nil {
		t.Fatalf("expected a retryable error on a transient Get failure, got nil")
	}
	if submitted != 0 {
		t.Errorf("Submit called %d times; a transient Get must not resubmit", submitted)
	}
	if name != "run-old" {
		t.Errorf("run name = %q, want the existing run-old preserved for re-attach", name)
	}
	if final != nil {
		t.Errorf("final status = %+v, want nil on a transient failure", final)
	}
}

// TestExecuteNotFoundResubmits covers the other half of F6: a genuine NotFound on
// the candidate means the run is gone, so submitting a fresh run is correct.
func TestExecuteNotFoundResubmits(t *testing.T) {
	submitted := 0
	f := RunFuncs{
		Get: func(_ context.Context, _ k8s.Connection, _, name string) (*RunStatus, error) {
			return nil, fmt.Errorf("tekton: get taskrun: %w", notFoundErr(name))
		},
		Submit: func(context.Context, k8s.Connection, string, RunSpec) (*RunStatus, error) {
			submitted++
			return &RunStatus{Name: "run-new"}, nil
		},
		Watch: func(context.Context, k8s.Connection, string, string) (*RunStream, error) {
			return &RunStream{Snapshot: RunStatus{Name: "run-new", Phase: "Succeeded"}}, nil
		},
	}
	name, final, err := f.Execute(context.Background(), RunRequest{ExistingRun: "run-old"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if submitted != 1 {
		t.Errorf("Submit called %d times; a NotFound candidate should resubmit once", submitted)
	}
	if name != "run-new" {
		t.Errorf("run name = %q, want run-new after resubmit", name)
	}
	if final == nil || !final.Terminal() {
		t.Errorf("final = %+v, want a terminal status", final)
	}
}

// TestExecuteActiveRunReattaches confirms a present, non-terminal candidate is
// re-attached (watched) rather than resubmitted.
func TestExecuteActiveRunReattaches(t *testing.T) {
	submitted := 0
	watched := ""
	f := RunFuncs{
		Get: func(_ context.Context, _ k8s.Connection, _, name string) (*RunStatus, error) {
			return &RunStatus{Name: name, Phase: "Running"}, nil
		},
		Submit: func(context.Context, k8s.Connection, string, RunSpec) (*RunStatus, error) {
			submitted++
			return &RunStatus{Name: "run-new"}, nil
		},
		Watch: func(_ context.Context, _ k8s.Connection, _, name string) (*RunStream, error) {
			watched = name
			return &RunStream{Snapshot: RunStatus{Name: name, Phase: "Succeeded"}}, nil
		},
	}
	name, final, err := f.Execute(context.Background(), RunRequest{ExistingRun: "run-old"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if submitted != 0 {
		t.Errorf("Submit called %d times; an active run must be re-attached, not resubmitted", submitted)
	}
	if name != "run-old" || watched != "run-old" {
		t.Errorf("name=%q watched=%q, want run-old re-attached", name, watched)
	}
	if final == nil || final.Phase != "Succeeded" {
		t.Errorf("final = %+v, want the watched terminal status", final)
	}
}
