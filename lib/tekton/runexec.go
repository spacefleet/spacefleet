package tekton

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/spacefleet/spacefleet/lib/k8s"
)

// This file holds the crash-safe single-run executor shared by every worker that
// drives one TaskRun to completion (the Helm rollout/preview workers and the
// workflow per-component executor). It owns the delicate part — re-attach to an
// in-flight run rather than spawning a duplicate, recover a run whose name was
// lost to a crash, and watch to a terminal phase — so that logic lives once.

// RunFuncs holds the four TaskRun primitives the executor needs. They are fields
// (not direct calls) so a worker can inject fakes in tests; in production a
// worker builds this from DefaultRunFuncs (or its own seam-aware wrappers).
type RunFuncs struct {
	Submit func(ctx context.Context, conn k8s.Connection, namespace string, spec RunSpec) (*RunStatus, error)
	Get    func(ctx context.Context, conn k8s.Connection, namespace, name string) (*RunStatus, error)
	Watch  func(ctx context.Context, conn k8s.Connection, namespace, name string) (*RunStream, error)
	List   func(ctx context.Context, conn k8s.Connection, namespace, selector string) ([]*RunStatus, error)
}

// DefaultRunFuncs wires the executor to the real lib/tekton primitives.
func DefaultRunFuncs() RunFuncs {
	return RunFuncs{Submit: SubmitTaskRun, Get: GetRun, Watch: WatchRun, List: ListRuns}
}

// RunRequest describes one crash-safe TaskRun execution.
type RunRequest struct {
	Conn      k8s.Connection
	Namespace string
	Spec      RunSpec
	// Labels are merged onto the spec at submit (e.g. RunOrgLabel, RunJobLabel,
	// and any per-run recovery label). The caller's spec is not mutated.
	Labels map[string]string
	// RecoverSelector finds an in-flight run a prior attempt already submitted but
	// whose name was lost in the window between submit and the name write — the run
	// carries the label, so it is recoverable without a name (e.g.
	// RunJobLabel+"="+jobID, or a per-component label for a workflow run). Empty
	// disables recovery.
	RecoverSelector string
	// ExistingRun is the persisted run name to re-attach to, if any.
	ExistingRun string
	// OnSubmitted persists the server-assigned run name immediately after a fresh
	// submit — before the (possibly long) await — so a live stream can find the
	// run. A non-nil error aborts the execution; the labelled run stays
	// recoverable on the next retry.
	OnSubmitted func(runName string) error
}

// Execute runs one TaskRun to a terminal phase, crash-safely. It re-attaches to
// ExistingRun (or a run recovered by RecoverSelector) while it is still in
// flight, otherwise submits a fresh labelled run, then watches to a terminal
// phase. It returns the run name (known once a run is attached or submitted; ""
// only when submit itself failed) and the terminal status.
//
// A non-nil error is retryable (submit/persist/watch failure, or a watch that
// ended before the run settled): the helm/kubectl scripts are idempotent and a
// present run is re-attached rather than resubmitted, so retries are safe. A
// settled run is returned with a non-nil status whose Phase may be "Failed" —
// the caller decides what a failed run means for its own state.
func (f RunFuncs) Execute(ctx context.Context, r RunRequest) (runName string, final *RunStatus, err error) {
	runName = r.ExistingRun
	if runName == "" && r.RecoverSelector != "" {
		runName = f.recover(ctx, r.Conn, r.Namespace, r.RecoverSelector)
	}
	// Decide whether a candidate run name (persisted or recovered) is one we can
	// re-attach to. With no candidate, submit straight away. With a candidate, a
	// not-found means it's gone and a fresh submit is correct; a transient Get
	// failure must NOT resubmit (that risks a duplicate TaskRun) — it returns a
	// retryable error so the job retries and re-attaches via the recovery selector.
	if runName != "" {
		active, aerr := f.runActive(ctx, r.Conn, r.Namespace, runName)
		if aerr != nil {
			return runName, nil, aerr
		}
		if !active {
			runName = ""
		}
	}
	if runName == "" {
		run, serr := f.Submit(ctx, r.Conn, r.Namespace, stampLabels(r.Spec, r.Labels))
		if serr != nil {
			return "", nil, serr
		}
		runName = run.Name
		if r.OnSubmitted != nil {
			if perr := r.OnSubmitted(runName); perr != nil {
				return runName, nil, perr
			}
		}
	}
	final, err = f.await(ctx, r.Conn, r.Namespace, runName)
	return runName, final, err
}

// recover finds a run a prior attempt already submitted under the selector and
// returns the newest still-active one, or "" (list error or none active) so the
// caller submits a fresh run.
func (f RunFuncs) recover(ctx context.Context, conn k8s.Connection, namespace, selector string) string {
	runs, err := f.List(ctx, conn, namespace, selector)
	if err != nil {
		return ""
	}
	return latestActiveRun(runs)
}

// runActive reports whether the named run is present and not yet terminal — the
// only state in which re-attaching (rather than resubmitting) is correct.
func (f RunFuncs) runActive(ctx context.Context, conn k8s.Connection, namespace, name string) bool {
	st, err := f.Get(ctx, conn, namespace, name)
	return err == nil && !st.Terminal()
}

// await watches the run to a terminal phase and returns its final status. If the
// watch ends before the run settles, a final Get decides: a still-non-terminal
// result is an error so the job retries (and re-attaches).
func (f RunFuncs) await(ctx context.Context, conn k8s.Connection, namespace, name string) (*RunStatus, error) {
	stream, err := f.Watch(ctx, conn, namespace, name)
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
				fin, gerr := f.Get(ctx, conn, namespace, name)
				if gerr != nil {
					return nil, gerr
				}
				if !fin.Terminal() {
					return nil, fmt.Errorf("tekton: watch on run %s ended before completion", name)
				}
				return fin, nil
			}
			run := ev.Object
			if run.Terminal() {
				return &run, nil
			}
		}
	}
}

// stampLabels returns spec with labels merged onto a fresh copy of spec.Labels,
// so the caller's RunSpec isn't mutated underneath it. Passed labels win over
// any pre-existing key.
func stampLabels(spec RunSpec, labels map[string]string) RunSpec {
	if len(labels) == 0 {
		return spec
	}
	merged := make(map[string]string, len(spec.Labels)+len(labels))
	for k, v := range spec.Labels {
		merged[k] = v
	}
	for k, v := range labels {
		merged[k] = v
	}
	spec.Labels = merged
	return spec
}

// latestActiveRun picks the run to re-attach to from a label-listed set: the most
// recently started run that has not reached a terminal phase. A crashed attempt
// is normally a single in-flight run; if more than one matches, the newest active
// one wins and the rest are left for the operator's pruner. Returns "" when none
// are still active (all terminal, or empty), so the caller submits a fresh run.
func latestActiveRun(runs []*RunStatus) string {
	var best *RunStatus
	for _, r := range runs {
		if r == nil || r.Terminal() {
			continue
		}
		if best == nil || startedAfter(r, best) {
			best = r
		}
	}
	if best == nil {
		return ""
	}
	return best.Name
}

// startedAfter reports whether a started later than b, treating a nil StartedAt
// (not yet scheduled) as earliest so a started run is preferred.
func startedAfter(a, b *RunStatus) bool {
	if a.StartedAt == nil {
		return false
	}
	if b.StartedAt == nil {
		return true
	}
	return a.StartedAt.After(*b.StartedAt)
}
