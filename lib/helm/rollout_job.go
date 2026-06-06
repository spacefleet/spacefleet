package helm

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/tekton"
)

// RunNamespace is the namespace on the runner cluster the rollout TaskRun is
// submitted to. The default namespace always exists; it matches the namespace
// lib/api uses for cluster TaskRuns so the live-run/log streams resolve there.
const RunNamespace = "default"

// rolloutWatchTimeout bounds the worker's wait for the run to settle. It must
// exceed the longest helm --wait (WaitTimeout), so River doesn't kill the watch
// before the step can finish.
const rolloutWatchTimeout = 35 * time.Minute

// previewWatchTimeout bounds the preview worker's wait. A diff has no helm
// --wait, so it settles fast; a run that hangs this long is wedged and should
// fail rather than tie up a worker.
const previewWatchTimeout = 10 * time.Minute

// RolloutPlan is everything the worker needs to run one rollout, resolved by the
// Store per attempt: the runner connection, the fully-rendered RunSpec (script +
// injected Files), and the application's current run name (for retry
// idempotency — see RolloutWorker.Work).
type RolloutPlan struct {
	RunnerConn  k8s.Connection
	RunSpec     tekton.RunSpec
	ExistingRun string
}

// Store is the persistence + resolution the rollout worker needs, satisfied by
// *applications.Service. Defining it here keeps the dependency one-way:
// lib/applications imports lib/helm, and the worker is handed a Store at
// construction (cmd/spacefleet/worker.go). The worker passes the org id from the
// job args so every call stays org-scoped.
type Store interface {
	// ResolveRollout resolves the runner connection, builds the target kubeconfig
	// (minting any cloud token late, per attempt), and assembles the RunSpec for
	// the given action (deploy/upgrade build `helm upgrade --install`; uninstall
	// builds `helm uninstall`).
	ResolveRollout(ctx context.Context, orgID, appID uuid.UUID, action string) (RolloutPlan, error)
	// MarkRollout persists a rollout-lifecycle transition: status is one of the
	// Status* constants; message/runName are set when non-empty (jobID likewise).
	MarkRollout(ctx context.Context, orgID, appID uuid.UUID, jobID, status, message, runName string) error
}

// PreviewStore is the persistence + resolution the preview (refresh) worker
// needs, satisfied by the same *applications.Service. It writes the sync_*
// columns, kept disjoint from the rollout columns the Store above writes.
type PreviewStore interface {
	// ResolvePreview is ResolveRollout's sibling for a diff: same connection,
	// kubeconfig, and injected files, but a RunSpec that runs `helm diff`.
	ResolvePreview(ctx context.Context, orgID, appID uuid.UUID) (PreviewPlan, error)
	// MarkPreview persists a non-terminal sync transition: status is one of the
	// Sync* constants (refreshing / error); message/runName set when non-empty.
	MarkPreview(ctx context.Context, orgID, appID uuid.UUID, jobID, status, message, runName string) error
	// CompletePreview records a successful diff run: it captures the run's logs,
	// parses the diff body + verdict + resolved revisions, and sets sync_status to
	// synced or out_of_sync. The worker can't decide synced/out_of_sync itself —
	// that verdict lives in the diff output, not the run phase.
	CompletePreview(ctx context.Context, orgID, appID uuid.UUID, jobID, runName string) error
}

// RolloutArgs is the River job enqueued by the API when an app is deployed,
// upgraded, or uninstalled. It carries only ids — never credentials; the worker
// re-opens sealed credentials through the Store.
type RolloutArgs struct {
	ApplicationID uuid.UUID `json:"application_id"`
	OrgID         uuid.UUID `json:"org_id"`
	Action        string    `json:"action"`
}

// Kind is the stable River job identifier.
func (RolloutArgs) Kind() string { return "helm_rollout" }

// PreviewPlan is everything the preview worker needs to run one diff, resolved
// by the PreviewStore per attempt. Mirrors RolloutPlan; ExistingRun is the app's
// sync_run_name (a preview retry re-attaches to the preview run, never a rollout).
type PreviewPlan struct {
	RunnerConn  k8s.Connection
	RunSpec     tekton.RunSpec
	ExistingRun string
}

// PreviewArgs is the River job enqueued by the API when an app is refreshed. It
// carries only ids; the worker re-opens sealed credentials through the PreviewStore.
type PreviewArgs struct {
	ApplicationID uuid.UUID `json:"application_id"`
	OrgID         uuid.UUID `json:"org_id"`
}

// Kind is the stable River job identifier.
func (PreviewArgs) Kind() string { return "helm_preview" }

// RolloutWorker runs a Helm rollout as a TaskRun on the app's runner cluster and
// reconciles the app's terminal status from the run's outcome. Because the helm
// step uses `--wait`, a Succeeded run means the release's resources became
// Ready, so the recorded status reflects the real rollout outcome.
type RolloutWorker struct {
	river.WorkerDefaults[RolloutArgs]
	Store Store

	// Test seams: default to the real lib/tekton calls; overridden in tests to
	// drive Work without a live cluster.
	submitFn func(ctx context.Context, conn k8s.Connection, namespace string, spec tekton.RunSpec) (*tekton.RunStatus, error)
	getFn    func(ctx context.Context, conn k8s.Connection, namespace, name string) (*tekton.RunStatus, error)
	watchFn  func(ctx context.Context, conn k8s.Connection, namespace, name string) (*tekton.RunStream, error)
}

// Timeout lets a rollout watch outlive River's default job timeout.
func (w *RolloutWorker) Timeout(*river.Job[RolloutArgs]) time.Duration {
	return rolloutWatchTimeout
}

func (w *RolloutWorker) submit(ctx context.Context, conn k8s.Connection, ns string, spec tekton.RunSpec) (*tekton.RunStatus, error) {
	if w.submitFn != nil {
		return w.submitFn(ctx, conn, ns, spec)
	}
	return tekton.SubmitTaskRun(ctx, conn, ns, spec)
}

func (w *RolloutWorker) getRun(ctx context.Context, conn k8s.Connection, ns, name string) (*tekton.RunStatus, error) {
	if w.getFn != nil {
		return w.getFn(ctx, conn, ns, name)
	}
	return tekton.GetRun(ctx, conn, ns, name)
}

func (w *RolloutWorker) watch(ctx context.Context, conn k8s.Connection, ns, name string) (*tekton.RunStream, error) {
	if w.watchFn != nil {
		return w.watchFn(ctx, conn, ns, name)
	}
	return tekton.WatchRun(ctx, conn, ns, name)
}

// Work runs one rollout: mark in-flight → resolve the plan → submit (or
// re-attach to an existing run on retry) → watch to a terminal phase → record
// the terminal app status. Returning a non-nil error lets River retry; the helm
// command is idempotent (`upgrade --install` / `uninstall --ignore-not-found`),
// and a present run is re-attached rather than resubmitted, so retries are safe.
func (w *RolloutWorker) Work(ctx context.Context, job *river.Job[RolloutArgs]) error {
	a := job.Args
	jobID := strconv.FormatInt(job.ID, 10)

	inFlight, terminalOK := StatusDeploying, StatusDeployed
	if a.Action == ActionUninstall {
		inFlight, terminalOK = StatusUninstalling, StatusUninstalled
	}
	_ = w.Store.MarkRollout(ctx, a.OrgID, a.ApplicationID, jobID, inFlight, "starting "+a.Action, "")

	plan, err := w.Store.ResolveRollout(ctx, a.OrgID, a.ApplicationID, a.Action)
	if err != nil {
		_ = w.Store.MarkRollout(ctx, a.OrgID, a.ApplicationID, jobID, StatusFailed, err.Error(), "")
		return err
	}

	// Idempotent submit: on a retry, re-attach to the run only while it's still
	// in flight, rather than spawning a duplicate TaskRun. A run that already
	// reached a terminal phase (e.g. the prior failed attempt) must not be
	// re-attached — that would just replay the old outcome — so a fresh rollout
	// submits a new run.
	runName := plan.ExistingRun
	if runName == "" || !w.runActive(ctx, plan.RunnerConn, runName) {
		run, serr := w.submit(ctx, plan.RunnerConn, RunNamespace, plan.RunSpec)
		if serr != nil {
			_ = w.Store.MarkRollout(ctx, a.OrgID, a.ApplicationID, jobID, StatusFailed, serr.Error(), "")
			return serr
		}
		runName = run.Name
		if err := w.Store.MarkRollout(ctx, a.OrgID, a.ApplicationID, jobID, inFlight, "submitted run "+runName, runName); err != nil {
			return err
		}
	}

	final, err := w.awaitRun(ctx, plan.RunnerConn, runName)
	if err != nil {
		_ = w.Store.MarkRollout(ctx, a.OrgID, a.ApplicationID, jobID, StatusFailed, err.Error(), runName)
		return err
	}
	if final.Phase == "Failed" {
		msg := final.Message
		if msg == "" {
			msg = "rollout run failed"
		}
		_ = w.Store.MarkRollout(ctx, a.OrgID, a.ApplicationID, jobID, StatusFailed, msg, runName)
		return fmt.Errorf("helm: rollout run %s failed: %s", runName, msg)
	}
	return w.Store.MarkRollout(ctx, a.OrgID, a.ApplicationID, jobID, terminalOK, final.Message, runName)
}

// runActive reports whether the named TaskRun is still present on the runner and
// has not yet reached a terminal phase — the only state in which re-attaching
// (rather than submitting a fresh run) is correct.
func (w *RolloutWorker) runActive(ctx context.Context, conn k8s.Connection, name string) bool {
	st, err := w.getRun(ctx, conn, RunNamespace, name)
	return err == nil && !st.Terminal()
}

// awaitRun watches the run until it reaches a terminal phase and returns its
// final status. If the watch ends before the run settles, it does a final Get;
// a still-non-terminal result is an error so River retries (and re-attaches).
func (w *RolloutWorker) awaitRun(ctx context.Context, conn k8s.Connection, name string) (*tekton.RunStatus, error) {
	stream, err := w.watch(ctx, conn, RunNamespace, name)
	if err != nil {
		return nil, err
	}
	if stream.Snapshot.Terminal() {
		return &stream.Snapshot, nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case ev, ok := <-stream.Events:
			if !ok {
				final, gerr := w.getRun(ctx, conn, RunNamespace, name)
				if gerr != nil {
					return nil, gerr
				}
				if !final.Terminal() {
					return nil, fmt.Errorf("helm: watch on run %s ended before completion", name)
				}
				return final, nil
			}
			run := ev.Object
			if run.Terminal() {
				return &run, nil
			}
		}
	}
}

// PreviewWorker runs a `helm diff` as a TaskRun on the app's runner cluster and
// records the resulting sync status (synced/out_of_sync) + diff. It mirrors
// RolloutWorker but writes the sync_* columns through a PreviewStore, leaving the
// rollout lifecycle untouched. The diff changes nothing in the target cluster.
type PreviewWorker struct {
	river.WorkerDefaults[PreviewArgs]
	Store PreviewStore

	// Test seams, as on RolloutWorker.
	submitFn func(ctx context.Context, conn k8s.Connection, namespace string, spec tekton.RunSpec) (*tekton.RunStatus, error)
	getFn    func(ctx context.Context, conn k8s.Connection, namespace, name string) (*tekton.RunStatus, error)
	watchFn  func(ctx context.Context, conn k8s.Connection, namespace, name string) (*tekton.RunStream, error)
}

// Timeout bounds the preview watch (shorter than a rollout's — no helm --wait).
func (w *PreviewWorker) Timeout(*river.Job[PreviewArgs]) time.Duration {
	return previewWatchTimeout
}

func (w *PreviewWorker) submit(ctx context.Context, conn k8s.Connection, ns string, spec tekton.RunSpec) (*tekton.RunStatus, error) {
	if w.submitFn != nil {
		return w.submitFn(ctx, conn, ns, spec)
	}
	return tekton.SubmitTaskRun(ctx, conn, ns, spec)
}

func (w *PreviewWorker) getRun(ctx context.Context, conn k8s.Connection, ns, name string) (*tekton.RunStatus, error) {
	if w.getFn != nil {
		return w.getFn(ctx, conn, ns, name)
	}
	return tekton.GetRun(ctx, conn, ns, name)
}

func (w *PreviewWorker) watch(ctx context.Context, conn k8s.Connection, ns, name string) (*tekton.RunStream, error) {
	if w.watchFn != nil {
		return w.watchFn(ctx, conn, ns, name)
	}
	return tekton.WatchRun(ctx, conn, ns, name)
}

// Work runs one preview: mark refreshing → resolve the plan → submit (or
// re-attach on retry) → watch to terminal → on success record the diff (the
// PreviewStore captures logs and derives synced/out_of_sync), on failure record
// the error. Returning non-nil lets River retry; `helm diff` is read-only and a
// present run is re-attached, so retries are safe.
func (w *PreviewWorker) Work(ctx context.Context, job *river.Job[PreviewArgs]) error {
	a := job.Args
	jobID := strconv.FormatInt(job.ID, 10)

	_ = w.Store.MarkPreview(ctx, a.OrgID, a.ApplicationID, jobID, SyncRefreshing, "starting refresh", "")

	plan, err := w.Store.ResolvePreview(ctx, a.OrgID, a.ApplicationID)
	if err != nil {
		_ = w.Store.MarkPreview(ctx, a.OrgID, a.ApplicationID, jobID, SyncError, err.Error(), "")
		return err
	}

	runName := plan.ExistingRun
	if runName == "" || !w.runActive(ctx, plan.RunnerConn, runName) {
		run, serr := w.submit(ctx, plan.RunnerConn, RunNamespace, plan.RunSpec)
		if serr != nil {
			_ = w.Store.MarkPreview(ctx, a.OrgID, a.ApplicationID, jobID, SyncError, serr.Error(), "")
			return serr
		}
		runName = run.Name
		if err := w.Store.MarkPreview(ctx, a.OrgID, a.ApplicationID, jobID, SyncRefreshing, "submitted run "+runName, runName); err != nil {
			return err
		}
	}

	final, err := w.awaitRun(ctx, plan.RunnerConn, runName)
	if err != nil {
		_ = w.Store.MarkPreview(ctx, a.OrgID, a.ApplicationID, jobID, SyncError, err.Error(), runName)
		return err
	}
	if final.Phase == "Failed" {
		msg := final.Message
		if msg == "" {
			msg = "diff run failed"
		}
		_ = w.Store.MarkPreview(ctx, a.OrgID, a.ApplicationID, jobID, SyncError, msg, runName)
		return fmt.Errorf("helm: diff run %s failed: %s", runName, msg)
	}
	return w.Store.CompletePreview(ctx, a.OrgID, a.ApplicationID, jobID, runName)
}

func (w *PreviewWorker) runActive(ctx context.Context, conn k8s.Connection, name string) bool {
	st, err := w.getRun(ctx, conn, RunNamespace, name)
	return err == nil && !st.Terminal()
}

func (w *PreviewWorker) awaitRun(ctx context.Context, conn k8s.Connection, name string) (*tekton.RunStatus, error) {
	stream, err := w.watch(ctx, conn, RunNamespace, name)
	if err != nil {
		return nil, err
	}
	if stream.Snapshot.Terminal() {
		return &stream.Snapshot, nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case ev, ok := <-stream.Events:
			if !ok {
				final, gerr := w.getRun(ctx, conn, RunNamespace, name)
				if gerr != nil {
					return nil, gerr
				}
				if !final.Terminal() {
					return nil, fmt.Errorf("helm: watch on run %s ended before completion", name)
				}
				return final, nil
			}
			run := ev.Object
			if run.Terminal() {
				return &run, nil
			}
		}
	}
}
