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

func (f *fakeStore) ResolveRollout(context.Context, uuid.UUID, uuid.UUID, string, bool) (RolloutPlan, error) {
	return f.plan, f.resolveErr
}

func (f *fakeStore) MarkRollout(_ context.Context, _, _ uuid.UUID, _, status, _, runName string) error {
	f.statuses = append(f.statuses, status)
	if runName != "" {
		f.lastRun = runName
	}
	return nil
}

// fakePreviewStore records the preview worker's calls.
type fakePreviewStore struct {
	plan       PreviewPlan
	resolveErr error
	statuses   []string // ordered status values passed to MarkPreview
	lastRun    string
	completed  bool // CompletePreview was called (terminal success)
}

func (f *fakePreviewStore) ResolvePreview(context.Context, uuid.UUID, uuid.UUID) (PreviewPlan, error) {
	return f.plan, f.resolveErr
}

func (f *fakePreviewStore) MarkPreview(_ context.Context, _, _ uuid.UUID, _, status, _, runName string) error {
	f.statuses = append(f.statuses, status)
	if runName != "" {
		f.lastRun = runName
	}
	return nil
}

func (f *fakePreviewStore) CompletePreview(context.Context, uuid.UUID, uuid.UUID, string, string) error {
	f.completed = true
	return nil
}

func previewJob() *river.Job[PreviewArgs] {
	return &river.Job[PreviewArgs]{
		JobRow: &rivertype.JobRow{ID: 9},
		Args:   PreviewArgs{ApplicationID: uuid.New(), OrgID: uuid.New()},
	}
}

func TestPreviewWorkerSuccess(t *testing.T) {
	store := &fakePreviewStore{}
	w := &PreviewWorker{
		Store: store,
		submitFn: func(context.Context, k8s.Connection, string, tekton.RunSpec) (*tekton.RunStatus, error) {
			return &tekton.RunStatus{Name: "helm-diff-1"}, nil
		},
		watchFn: func(context.Context, k8s.Connection, string, string) (*tekton.RunStream, error) {
			return terminalStream("helm-diff-1", "Succeeded", "ok"), nil
		},
	}
	if err := w.Work(context.Background(), previewJob()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if !store.completed {
		t.Error("expected CompletePreview to be called on a successful diff run")
	}
	if !containsPreviewStatus(store, SyncRefreshing) {
		t.Error("expected a refreshing status")
	}
	if store.lastRun != "helm-diff-1" {
		t.Errorf("recorded run = %q", store.lastRun)
	}
}

func TestPreviewWorkerRunFailed(t *testing.T) {
	store := &fakePreviewStore{}
	w := &PreviewWorker{
		Store: store,
		submitFn: func(context.Context, k8s.Connection, string, tekton.RunSpec) (*tekton.RunStatus, error) {
			return &tekton.RunStatus{Name: "helm-diff-2"}, nil
		},
		watchFn: func(context.Context, k8s.Connection, string, string) (*tekton.RunStream, error) {
			return terminalStream("helm-diff-2", "Failed", "boom"), nil
		},
	}
	if err := w.Work(context.Background(), previewJob()); err == nil {
		t.Fatal("expected an error so River retries")
	}
	if store.completed {
		t.Error("a failed run must not complete the preview")
	}
	if last := lastPreviewStatus(store); last != SyncError {
		t.Errorf("final status = %q, want %q", last, SyncError)
	}
}

func TestPreviewWorkerResolveFails(t *testing.T) {
	store := &fakePreviewStore{resolveErr: errors.New("decrypt failed")}
	w := &PreviewWorker{Store: store}
	if err := w.Work(context.Background(), previewJob()); err == nil {
		t.Fatal("expected an error")
	}
	if last := lastPreviewStatus(store); last != SyncError {
		t.Errorf("final status = %q, want %q", last, SyncError)
	}
}

func lastPreviewStatus(f *fakePreviewStore) string {
	if len(f.statuses) == 0 {
		return ""
	}
	return f.statuses[len(f.statuses)-1]
}

func containsPreviewStatus(f *fakePreviewStore, s string) bool {
	for _, v := range f.statuses {
		if v == s {
			return true
		}
	}
	return false
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

// TestRolloutWorkerReattach: when the plan carries an existing run that is still
// in flight, the worker re-attaches (watches) instead of resubmitting.
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
			return &tekton.RunStatus{Name: "old-run", Phase: "Running"}, nil // still in flight
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

// TestRolloutWorkerResubmitsAfterTerminalRun: a re-deploy whose existing run
// already reached a terminal phase (e.g. the prior failed attempt) must submit a
// fresh run, not re-attach and replay the old outcome.
func TestRolloutWorkerResubmitsAfterTerminalRun(t *testing.T) {
	store := &fakeStore{plan: RolloutPlan{ExistingRun: "old-run"}}
	submitCalls := 0
	w := &RolloutWorker{
		Store: store,
		submitFn: func(context.Context, k8s.Connection, string, tekton.RunSpec) (*tekton.RunStatus, error) {
			submitCalls++
			return &tekton.RunStatus{Name: "new-run"}, nil
		},
		getFn: func(context.Context, k8s.Connection, string, string) (*tekton.RunStatus, error) {
			return &tekton.RunStatus{Name: "old-run", Phase: "Failed"}, nil // terminal
		},
		watchFn: func(_ context.Context, _ k8s.Connection, _, name string) (*tekton.RunStream, error) {
			return terminalStream(name, "Succeeded", ""), nil
		},
	}
	if err := w.Work(context.Background(), rolloutJob(ActionDeploy)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if submitCalls != 1 {
		t.Errorf("expected a fresh submit after a terminal run, got %d submit calls", submitCalls)
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
