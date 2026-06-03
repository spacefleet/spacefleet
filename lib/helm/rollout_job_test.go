package helm

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/tekton"
)

// fakeStore records MarkRollout calls and answers ResolveRollout.
type fakeStore struct {
	plan       RolloutPlan
	resolveErr error
	statuses   []string // ordered status values passed to MarkRollout
	lastRun    string   // last non-empty runName recorded
}

func (f *fakeStore) ResolveRollout(context.Context, uuid.UUID, uuid.UUID, string) (RolloutPlan, error) {
	return f.plan, f.resolveErr
}

func (f *fakeStore) MarkRollout(_ context.Context, _, _ uuid.UUID, _, status, _, runName string) error {
	f.statuses = append(f.statuses, status)
	if runName != "" {
		f.lastRun = runName
	}
	return nil
}

func rolloutJob(action string) *river.Job[RolloutArgs] {
	return &river.Job[RolloutArgs]{
		JobRow: &rivertype.JobRow{ID: 7},
		Args:   RolloutArgs{ApplicationID: uuid.New(), OrgID: uuid.New(), Action: action},
	}
}

func terminalStream(name, phase, msg string) *tekton.RunStream {
	ch := make(chan k8s.Event[tekton.RunStatus])
	close(ch)
	return &tekton.RunStream{Snapshot: tekton.RunStatus{Name: name, Phase: phase, Message: msg}, Events: ch}
}

func TestRolloutWorkerDeploySuccess(t *testing.T) {
	store := &fakeStore{}
	submitCalls := 0
	w := &RolloutWorker{
		Store: store,
		submitFn: func(context.Context, k8s.Connection, string, tekton.RunSpec) (*tekton.RunStatus, error) {
			submitCalls++
			return &tekton.RunStatus{Name: "helm-run-1"}, nil
		},
		watchFn: func(context.Context, k8s.Connection, string, string) (*tekton.RunStream, error) {
			return terminalStream("helm-run-1", "Succeeded", "ok"), nil
		},
	}
	if err := w.Work(context.Background(), rolloutJob(ActionDeploy)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if last := lastStatus(store); last != StatusDeployed {
		t.Errorf("final status = %q, want %q", last, StatusDeployed)
	}
	if !containsStatus(store, StatusDeploying) {
		t.Error("expected a deploying status")
	}
	if store.lastRun != "helm-run-1" {
		t.Errorf("recorded run = %q", store.lastRun)
	}
	if submitCalls != 1 {
		t.Errorf("submit called %d times, want 1", submitCalls)
	}
}

func TestRolloutWorkerRunFailed(t *testing.T) {
	store := &fakeStore{}
	w := &RolloutWorker{
		Store: store,
		submitFn: func(context.Context, k8s.Connection, string, tekton.RunSpec) (*tekton.RunStatus, error) {
			return &tekton.RunStatus{Name: "helm-run-2"}, nil
		},
		watchFn: func(context.Context, k8s.Connection, string, string) (*tekton.RunStream, error) {
			return terminalStream("helm-run-2", "Failed", "image pull error"), nil
		},
	}
	err := w.Work(context.Background(), rolloutJob(ActionDeploy))
	if err == nil {
		t.Fatal("expected an error so River retries")
	}
	if last := lastStatus(store); last != StatusFailed {
		t.Errorf("final status = %q, want %q", last, StatusFailed)
	}
}

func TestRolloutWorkerResolveFails(t *testing.T) {
	store := &fakeStore{resolveErr: errors.New("decrypt failed")}
	w := &RolloutWorker{Store: store}
	if err := w.Work(context.Background(), rolloutJob(ActionDeploy)); err == nil {
		t.Fatal("expected an error")
	}
	if last := lastStatus(store); last != StatusFailed {
		t.Errorf("final status = %q, want %q", last, StatusFailed)
	}
}

func TestRolloutWorkerUninstall(t *testing.T) {
	store := &fakeStore{}
	w := &RolloutWorker{
		Store: store,
		submitFn: func(context.Context, k8s.Connection, string, tekton.RunSpec) (*tekton.RunStatus, error) {
			return &tekton.RunStatus{Name: "helm-run-3"}, nil
		},
		watchFn: func(context.Context, k8s.Connection, string, string) (*tekton.RunStream, error) {
			return terminalStream("helm-run-3", "Succeeded", ""), nil
		},
	}
	if err := w.Work(context.Background(), rolloutJob(ActionUninstall)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if last := lastStatus(store); last != StatusUninstalled {
		t.Errorf("final status = %q, want %q", last, StatusUninstalled)
	}
	if !containsStatus(store, StatusUninstalling) {
		t.Error("expected an uninstalling status")
	}
}

// TestRolloutWorkerReattach: when the plan carries an existing run that still
// exists, the worker re-attaches (watches) instead of resubmitting.
func TestRolloutWorkerReattach(t *testing.T) {
	store := &fakeStore{plan: RolloutPlan{ExistingRun: "old-run"}}
	submitCalls := 0
	w := &RolloutWorker{
		Store: store,
		submitFn: func(context.Context, k8s.Connection, string, tekton.RunSpec) (*tekton.RunStatus, error) {
			submitCalls++
			return &tekton.RunStatus{Name: "new-run"}, nil
		},
		getFn: func(context.Context, k8s.Connection, string, string) (*tekton.RunStatus, error) {
			return &tekton.RunStatus{Name: "old-run"}, nil // exists
		},
		watchFn: func(_ context.Context, _ k8s.Connection, _, name string) (*tekton.RunStream, error) {
			return terminalStream(name, "Succeeded", ""), nil
		},
	}
	if err := w.Work(context.Background(), rolloutJob(ActionDeploy)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if submitCalls != 0 {
		t.Errorf("expected re-attach (no submit), got %d submit calls", submitCalls)
	}
	if lastStatus(store) != StatusDeployed {
		t.Errorf("final status = %q, want deployed", lastStatus(store))
	}
}

func lastStatus(f *fakeStore) string {
	if len(f.statuses) == 0 {
		return ""
	}
	return f.statuses[len(f.statuses)-1]
}

func containsStatus(f *fakeStore, s string) bool {
	for _, v := range f.statuses {
		if v == s {
			return true
		}
	}
	return false
}
