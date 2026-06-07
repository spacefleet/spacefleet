package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/componentrun"
	"github.com/spacefleet/spacefleet/lib/deploy"
	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/tekton"
)

// workflowWatchTimeout bounds the worker's wait for the whole DAG to settle. It
// must comfortably exceed the longest single component's helm --wait
// (helm.WaitTimeout) since components after a gate run sequentially; the run
// itself is the long-lived unit here, so this is generous.
const workflowWatchTimeout = 90 * time.Minute

// maxComponentConcurrency caps how many components run at once behind the
// scheduler's semaphore, so a wide fan-out can't flood the runner cluster with
// TaskRuns. The effective bound is min(this, number of nodes).
const maxComponentConcurrency = 8

// maxLogBytes caps a captured component run's stored logs so a pathological run
// can't bloat the row or memory. Mirrors the cap in lib/applications.
const maxLogBytes = 1024 * 1024

// runLogsFn captures a terminal run's full pod logs from the runner connection,
// best-effort. It is a seam (defaults to the real lib/tekton + lib/k8s path)
// overridden in tests so the worker runs without a live cluster. Returns "" on
// any failure — log capture never fails the run.
type runLogsFn func(ctx context.Context, runnerConn k8s.Connection, runName string) string

// WorkflowRunWorker executes one workflow run: it loads the run's graph snapshot
// + its ComponentRuns + the application, then drives the DAG through the pure
// scheduler, running each node as a crash-safe TaskRun on the app's runner
// cluster via the shared tekton executor. Each component is labelled with its
// ComponentRun id (tekton.RunComponentLabel) so a retry after a crash re-attaches
// to the right in-flight TaskRun rather than double-submitting — the make-or-break
// recovery key, since one workflow River job submits many TaskRuns under one job id.
type WorkflowRunWorker struct {
	river.WorkerDefaults[WorkflowRunArgs]

	svc      *Service
	resolver *deploy.Resolver

	// Test seams. runFuncs defaults to the real tekton primitives; captureLogs to
	// the real pod-log read. Overridden in tests to drive Work without a cluster.
	funcs       tekton.RunFuncs
	captureLogs runLogsFn
}

// NewWorker builds the workflow run worker over the workflow service and the
// shared run-input resolver (the same deps the applications service holds). It
// wires the real tekton executor and log capture; tests override the seams.
func NewWorker(svc *Service, resolver *deploy.Resolver) *WorkflowRunWorker {
	w := &WorkflowRunWorker{svc: svc, resolver: resolver, funcs: tekton.DefaultRunFuncs()}
	w.captureLogs = defaultCaptureLogs
	return w
}

// Timeout lets a workflow run watch outlive River's default job timeout.
func (w *WorkflowRunWorker) Timeout(*river.Job[WorkflowRunArgs]) time.Duration {
	return workflowWatchTimeout
}

// Work runs one workflow run end to end. It loads the run, its component runs, and
// the app; marks the run running; drives the DAG through schedule(); then marks
// the run with the scheduler's terminal status. It returns nil for
// succeeded/partial and an error for failed so River retries the job — retries are
// safe because already-terminal components short-circuit and in-flight TaskRuns
// re-attach (never double-submit) via the per-component label.
func (w *WorkflowRunWorker) Work(ctx context.Context, job *river.Job[WorkflowRunArgs]) (err error) {
	a := job.Args

	// Crash safety (F5): a panic anywhere below would otherwise unwind Work without
	// ever marking the run terminal, leaving it stuck "running" forever (River sees a
	// panicking job as an error and may retry, but each retry would re-panic and the
	// run row would never settle). Recover, mark the run failed, and re-surface the
	// panic as a returned error so River records the failure. A hard kill (SIGKILL,
	// node loss) can't run this defer — the periodic reaper settles those runs.
	defer func() {
		if r := recover(); r != nil {
			_ = w.svc.MarkRun(ctx, a.OrgID, a.WorkflowRunID, "failed", fmt.Sprintf("workflow run panicked: %v", r))
			err = fmt.Errorf("workflows: run %s panicked: %v", a.WorkflowRunID, r)
		}
	}()

	run, comps, err := w.svc.GetRun(ctx, a.OrgID, a.ApplicationID, a.WorkflowRunID)
	if err != nil {
		return err
	}
	app, err := w.svc.getApp(ctx, a.OrgID, a.ApplicationID)
	if err != nil {
		return err
	}

	// Parse the graph snapshot (not the live components) so an in-flight run is
	// immune to edits of the workflow.
	var snapshot GraphSnapshot
	if run.Graph != "" {
		if err := json.Unmarshal([]byte(run.Graph), &snapshot); err != nil {
			_ = w.svc.MarkRun(ctx, a.OrgID, a.WorkflowRunID, "failed", "invalid graph snapshot: "+err.Error())
			return fmt.Errorf("workflows: parse graph snapshot for run %s: %w", a.WorkflowRunID, err)
		}
	}

	// Map each snapshot node to its ComponentRun (by component id), so the executor
	// has the durable per-step row to mark and to recover by.
	crByComponent := make(map[uuid.UUID]*ent.ComponentRun, len(comps))
	for _, cr := range comps {
		crByComponent[cr.ComponentID] = cr
	}
	nodeByID := make(map[uuid.UUID]GraphNode, len(snapshot.Nodes))
	for _, n := range snapshot.Nodes {
		nodeByID[n.ID] = n
	}

	_ = w.svc.MarkRun(ctx, a.OrgID, a.WorkflowRunID, "running", "running workflow")

	// Build the scheduler nodes from the snapshot. For a preview run we deliberately
	// clear DependsOn so every component's dry-run is independently runnable and they
	// all run concurrently (up to the concurrency cap) — the documented preview
	// semantics. Honoring the DAG would gain nothing: a dry-run can't see an upstream
	// step's effects anyway (cross-component blindness — e.g. a manifest needing a CRD
	// an upstream helm release would install), so gating downstream dry-runs on
	// upstream "passes" would only serialize them for no benefit. A dry-run that errors
	// marks just that component failed; clearing deps already lets the others still
	// run, so ContinueOnFailure is irrelevant here. Deploy/uninstall keep the DAG deps.
	preview := a.Action == ActionPreview
	nodes := make([]schedNode, 0, len(snapshot.Nodes))
	for _, n := range snapshot.Nodes {
		cr := crByComponent[n.ID]
		sn := schedNode{
			ID:                n.ID,
			DependsOn:         n.DependsOn,
			ContinueOnFailure: n.ContinueOnFailure,
			RequiresApproval:  n.RequiresApproval,
			// A gate that was already approved on a prior (parked) attempt carries an
			// approved_at; treat it as approved so a resumed run launches it instead of
			// re-parking. Missing component runs (defensive) read as not-approved.
			Approved: cr != nil && cr.ApprovedAt != nil && !cr.ApprovedAt.IsZero(),
		}
		if preview {
			// Preview is a whole-workflow dry-run: every node runs independently and
			// approval gates do not apply (there is nothing to apply to a cluster), so
			// clear both the DAG deps and the gate.
			sn.DependsOn = nil
			sn.RequiresApproval = false
		}
		nodes = append(nodes, sn)
	}

	concurrency := len(nodes)
	if concurrency > maxComponentConcurrency {
		concurrency = maxComponentConcurrency
	}

	// ran records the node ids runFn actually executed (and thus already persisted a
	// terminal component_run for). The scheduler also settles nodes it never runs —
	// "skipped" dependents, and "failed" for a node referencing an unknown dependency
	// id (F1) — and onState persists those; ran lets onState tell a scheduler-settled
	// "failed" apart from a runFn failure so it doesn't double-mark. Guarded by ranMu
	// since runFn runs concurrently across nodes.
	var (
		ranMu        sync.Mutex
		ran          = make(map[uuid.UUID]struct{}, len(snapshot.Nodes))
		retryable    bool // set if any component failed for a transient/infra reason (F4)
		retryableErr error
	)
	runFn := func(ctx context.Context, sn schedNode) nodeResult {
		ranMu.Lock()
		ran[sn.ID] = struct{}{}
		ranMu.Unlock()
		node, ok := nodeByID[sn.ID]
		if !ok {
			return nodeResult{Status: statusFailed, Err: fmt.Errorf("workflows: node %s missing from snapshot", sn.ID)}
		}
		cr, ok := crByComponent[sn.ID]
		if !ok {
			return nodeResult{Status: statusFailed, Err: fmt.Errorf("workflows: component run for node %s missing", sn.ID)}
		}
		res := w.runComponent(ctx, a, app, node, cr, nodeByID)
		if res.Status == statusFailed && res.Retryable {
			ranMu.Lock()
			retryable = true
			if retryableErr == nil {
				retryableErr = res.Err
			}
			ranMu.Unlock()
		}
		return res
	}

	onState := func(id uuid.UUID, status string) {
		// runFn persists running/succeeded/failed itself for every node it actually
		// runs, so we only persist the transitions the scheduler drives without
		// invoking runFn: "skipped" (a dep hard-failed or was skipped) and the
		// "failed" it settles directly when a node references an unknown dependency id
		// (F1) — that node never reaches runFn, so without persisting here its
		// component_run would be left pending while the run reports failed.
		cr, ok := crByComponent[id]
		if !ok {
			return
		}
		switch status {
		case statusAwaitingApproval:
			// The scheduler parked a ready, gated node (RequiresApproval && !Approved)
			// instead of running it. Persist the gate so the run view shows it paused and
			// the approve/reject handler has a component run to act on. The node never
			// reaches runFn while parked, so this is the only writer of the status.
			_ = w.svc.MarkComponentRun(ctx, a.OrgID, cr.ID, "awaiting_approval", "awaiting manual approval", "")
		case statusSkipped:
			_ = w.svc.MarkComponentRun(ctx, a.OrgID, cr.ID, "skipped", "skipped (an upstream component did not pass)", "")
		case statusFailed:
			// runFn persists its own "failed" (with the real error). A "failed" the
			// scheduler emitted for a node it never ran is the unknown-dependency case
			// (F1); persist it so the component_run reflects the failed run.
			ranMu.Lock()
			_, executed := ran[id]
			ranMu.Unlock()
			if !executed {
				_ = w.svc.MarkComponentRun(ctx, a.OrgID, cr.ID, "failed", "unresolved dependency: a referenced component is not part of this run", "")
			}
		}
	}

	final := schedule(ctx, nodes, concurrency, runFn, onState)

	// Suspended: one or more gated nodes parked awaiting approval and the rest of the
	// DAG drained as far as it could. Park the run (not terminal) and return nil so
	// River considers this job done — there is nothing to retry. The approve/reject
	// handler enqueues a fresh resume job that re-drives the DAG with the gate cleared
	// (already-terminal components short-circuit; the approved gate now launches).
	if final == runSuspended {
		_ = w.svc.MarkRun(ctx, a.OrgID, a.WorkflowRunID, "awaiting_approval", "awaiting manual approval")
		return nil
	}

	msg := "workflow " + final
	_ = w.svc.MarkRun(ctx, a.OrgID, a.WorkflowRunID, final, msg)

	// Settle any component run the scheduler left non-terminal. The case that bites
	// is a gated node that parked at awaiting_approval on a branch while a parallel
	// branch hard-failed: hardFailed outranks suspended, so schedule() returns
	// runFailed/runPartial and the parked node is never settled. The run is now
	// terminal, but its parked step would otherwise sit at awaiting_approval forever
	// with no recovery path (approve/reject and cancel all require an in-flight run).
	// Sweep those to skipped, mirroring CancelRun's settle-steps. Terminal steps are
	// untouched, so genuine succeeded/failed/skipped results are preserved.
	if final == runFailed || final == runPartial {
		_, _ = w.svc.SettleStuckComponentRuns(ctx, a.OrgID, a.WorkflowRunID, "skipped (run did not complete)")
	}
	if final == runFailed {
		// F4: only return an error (so River retries the whole job) when at least one
		// component failed for a genuinely retryable reason — a transient/infra
		// condition where re-running can change the outcome (e.g. the TaskRun couldn't
		// be submitted or watched), or a context cancellation/timeout. The retry is
		// idempotent: already-terminal components short-circuit and in-flight ones
		// re-attach via the per-component label, so only the unsettled work re-runs.
		//
		// When the failure is fully attributable to settled component results that
		// won't change on retry — a component's TaskRun ran to a terminal Failed phase,
		// or a deterministic plan/config error — return nil. The run is already marked
		// failed; retrying would only re-run the same failing scripts ~25 times.
		if ctx.Err() != nil {
			return fmt.Errorf("workflows: run %s aborted: %w", a.WorkflowRunID, ctx.Err())
		}
		if retryable {
			return fmt.Errorf("workflows: run %s failed (retryable): %w", a.WorkflowRunID, retryableErr)
		}
		return nil
	}
	return nil
}

// runComponent executes a single component to terminal and returns its scheduler
// outcome. It short-circuits a component already terminal in the DB (a retry must
// not re-run a succeeded component), marks it running, builds the RunSpec via the
// planner, executes the TaskRun crash-safely (re-attaching by the per-component
// label), captures logs + revisions, and marks the terminal status.
func (w *WorkflowRunWorker) runComponent(ctx context.Context, a WorkflowRunArgs, app *ent.Application, node GraphNode, cr *ent.ComponentRun, byID map[uuid.UUID]GraphNode) nodeResult {
	// Retry short-circuit: a component already settled in a prior attempt is not
	// re-run; its stored status drives the scheduler so dependents see the same
	// outcome. Skipped is treated as a non-pass terminal too.
	switch cr.Status {
	case componentrun.StatusSucceeded:
		return nodeResult{Status: statusSucceeded}
	case componentrun.StatusFailed:
		return nodeResult{Status: statusFailed}
	case componentrun.StatusSkipped:
		return nodeResult{Status: statusSkipped}
	}

	_ = w.svc.MarkComponentRun(ctx, a.OrgID, cr.ID, "running", "starting "+a.Action, "")

	req, err := w.planComponent(ctx, app, node, a.Action, a.Force, cr.RunName, a.WorkflowRunID, byID)
	if err != nil {
		_ = w.svc.MarkComponentRun(ctx, a.OrgID, cr.ID, "failed", err.Error(), "")
		return nodeResult{Status: statusFailed, Err: err}
	}

	// Stamp the org, workflow-run, and per-component labels and set the recovery
	// selector to the per-component label so a crashed attempt re-attaches to the
	// right in-flight TaskRun (R1). OnSubmitted persists the assigned run name
	// immediately so a live stream and the next retry can find it.
	req.Labels = map[string]string{
		tekton.RunOrgLabel:       a.OrgID.String(),
		tekton.RunJobLabel:       a.WorkflowRunID.String(),
		tekton.RunComponentLabel: cr.ID.String(),
	}
	req.RecoverSelector = tekton.RunComponentLabel + "=" + cr.ID.String()
	req.OnSubmitted = func(runName string) error {
		return w.svc.MarkComponentRun(ctx, a.OrgID, cr.ID, "running", "submitted run "+runName, runName)
	}

	runnerConn := req.Conn
	runName, finalStatus, err := w.funcs.Execute(ctx, req)
	if err != nil {
		// The executor failed to submit or watch the TaskRun to terminal — an
		// infra/transient condition (cluster unreachable, API error), not a settled
		// component result. Mark it retryable so the run's River job retries (F4): the
		// next attempt re-attaches to any in-flight TaskRun via the per-component label.
		_ = w.svc.MarkComponentRun(ctx, a.OrgID, cr.ID, "failed", err.Error(), runName)
		return nodeResult{Status: statusFailed, Err: err, Retryable: true}
	}

	// Terminal: capture logs + resolved revisions best-effort before settling.
	logs := ""
	if w.captureLogs != nil {
		logs = w.captureLogs(ctx, runnerConn, runName)
	}
	if logs != "" {
		rev := helm.ParseRevisions(logs)
		_ = w.svc.SetComponentRunLogs(ctx, a.OrgID, cr.ID, logs, rev.Chart, encodeValuesRevision(rev.Values))
	}

	if finalStatus.Phase == "Failed" {
		msg := finalStatus.Message
		if msg == "" {
			msg = "component run failed"
		}
		_ = w.svc.MarkComponentRun(ctx, a.OrgID, cr.ID, "failed", msg, runName)
		return nodeResult{Status: statusFailed, Err: fmt.Errorf("workflows: component %q run %s failed: %s", node.Name, runName, msg)}
	}
	_ = w.svc.MarkComponentRun(ctx, a.OrgID, cr.ID, "succeeded", finalStatus.Message, runName)
	return nodeResult{Status: statusSucceeded}
}

// encodeValuesRevision serializes the resolved values-source revisions (the
// index→SHA map helm.ParseRevisions extracts from the run logs) into the string
// stored on component_runs.values_revision (F15). It's a JSON object keyed by the
// values-source index so a viewer can line each resolved commit up with its
// ordered source. An empty map yields "" so SetComponentRunLogs leaves the column
// untouched (no values-from-git sources resolved). encoding/json sorts integer-ish
// map keys, so the output is deterministic across attempts.
func encodeValuesRevision(values map[int]string) string {
	if len(values) == 0 {
		return ""
	}
	b, err := json.Marshal(values)
	if err != nil {
		// map[int]string always marshals; treat an impossible error as "no revision"
		// rather than failing the run (revision capture is best-effort).
		return ""
	}
	return string(b)
}

// defaultCaptureLogs reads a terminal run's full pod logs from the runner
// connection, capped, no follow — best-effort, "" on any failure. Mirrors
// applications.fetchRunLogs but works straight off the resolved runner connection
// (the planner already resolved it), so the worker needs no extra cluster lookup.
func defaultCaptureLogs(ctx context.Context, runnerConn k8s.Connection, runName string) string {
	if runName == "" {
		return ""
	}
	run, err := tekton.GetRun(ctx, runnerConn, helm.RunNamespace, runName)
	if err != nil || run.PodName == "" {
		return ""
	}
	rc, err := k8s.StreamPodLogs(ctx, runnerConn, helm.RunNamespace, run.PodName, k8s.LogOptions{Follow: false})
	if err != nil {
		return ""
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, maxLogBytes+1))
	if err != nil {
		return ""
	}
	if len(b) > maxLogBytes {
		return string(b[:maxLogBytes]) + "\n... logs truncated ..."
	}
	return string(b)
}
