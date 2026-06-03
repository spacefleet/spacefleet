package tekton

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/spacefleet/spacefleet/lib/k8s"
)

// fakeStore records MarkTekton calls and answers ConnForTekton.
type fakeStore struct {
	conn     k8s.Connection
	connErr  error
	statuses []string // ordered status values passed to MarkTekton
}

func (f *fakeStore) ConnForTekton(context.Context, uuid.UUID, uuid.UUID) (k8s.Connection, error) {
	return f.conn, f.connErr
}

func (f *fakeStore) MarkTekton(_ context.Context, _, _ uuid.UUID, _, status, _, _ string) error {
	f.statuses = append(f.statuses, status)
	return nil
}

func installJob(action string) *river.Job[InstallArgs] {
	return &river.Job[InstallArgs]{
		JobRow: &rivertype.JobRow{ID: 42},
		Args:   InstallArgs{ClusterID: uuid.New(), OrgID: uuid.New(), Action: action},
	}
}

func TestInstallWorkerSuccess(t *testing.T) {
	store := &fakeStore{}
	w := &InstallWorker{
		Store:     store,
		installFn: func(context.Context, k8s.Connection, func(string)) (string, error) { return PinnedVersion, nil },
	}
	if err := w.Work(context.Background(), installJob(ActionInstall)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if got := lastStatus(store); got != StatusInstalled {
		t.Errorf("final status = %q, want %q", got, StatusInstalled)
	}
	if !containsStatus(store, StatusInstalling) {
		t.Error("expected an installing status before installed")
	}
}

func TestInstallWorkerInstallFails(t *testing.T) {
	store := &fakeStore{}
	w := &InstallWorker{
		Store: store,
		installFn: func(context.Context, k8s.Connection, func(string)) (string, error) {
			return "", errors.New("forbidden")
		},
	}
	err := w.Work(context.Background(), installJob(ActionInstall))
	if err == nil {
		t.Fatal("expected an error so River retries")
	}
	if got := lastStatus(store); got != StatusFailed {
		t.Errorf("final status = %q, want %q", got, StatusFailed)
	}
}

func TestInstallWorkerConnFails(t *testing.T) {
	store := &fakeStore{connErr: errors.New("decrypt failed")}
	w := &InstallWorker{Store: store}
	if err := w.Work(context.Background(), installJob(ActionInstall)); err == nil {
		t.Fatal("expected an error")
	}
	if got := lastStatus(store); got != StatusFailed {
		t.Errorf("final status = %q, want %q", got, StatusFailed)
	}
}

func TestInstallWorkerUninstall(t *testing.T) {
	store := &fakeStore{}
	w := &InstallWorker{
		Store:       store,
		uninstallFn: func(context.Context, k8s.Connection, func(string)) error { return nil },
	}
	if err := w.Work(context.Background(), installJob(ActionUninstall)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if got := lastStatus(store); got != StatusNotInstalled {
		t.Errorf("final status = %q, want %q", got, StatusNotInstalled)
	}
}

// TestInstallWorkerUpgrade runs the same apply as install but reports the
// upgrading lifecycle status, never installing.
func TestInstallWorkerUpgrade(t *testing.T) {
	store := &fakeStore{}
	w := &InstallWorker{
		Store:     store,
		installFn: func(context.Context, k8s.Connection, func(string)) (string, error) { return PinnedVersion, nil },
	}
	if err := w.Work(context.Background(), installJob(ActionUpgrade)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if got := lastStatus(store); got != StatusInstalled {
		t.Errorf("final status = %q, want %q", got, StatusInstalled)
	}
	if !containsStatus(store, StatusUpgrading) {
		t.Error("expected an upgrading status")
	}
	if containsStatus(store, StatusInstalling) {
		t.Error("upgrade should report upgrading, not installing")
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
