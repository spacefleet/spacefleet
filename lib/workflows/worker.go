package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
func (w *WorkflowRunWorker) Work(ctx context.Context, job *river.Job[WorkflowRunArgs]) error {
	a := job.Args

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
		sn := schedNode{
			ID:                n.ID,
			DependsOn:         n.DependsOn,
			ContinueOnFailure: n.ContinueOnFailure,
		}
		if preview {
			sn.DependsOn = nil
		}
		nodes = append(nodes, sn)
	}

	concurrency := len(nodes)
	if concurrency > maxComponentConcurrency {
		concurrency = maxComponentConcurrency
	}

	runFn := func(ctx context.Context, sn schedNode) nodeResult {
		node, ok := nodeByID[sn.ID]
		if !ok {
			return nodeResult{Status: statusFailed, Err: fmt.Errorf("workflows: node %s missing from snapshot", sn.ID)}
		}
		cr, ok := crByComponent[sn.ID]
		if !ok {
			return nodeResult{Status: statusFailed, Err: fmt.Errorf("workflows: component run for node %s missing", sn.ID)}
		}
		return w.runComponent(ctx, a, app, node, cr)
	}

	onState := func(id uuid.UUID, status string) {
		// The scheduler emits "skipped" for nodes it never runs (a dep hard-failed or
		// was skipped); runFn already persists running/succeeded/failed itself, so we
		// only need to persist skips here. "running" is also driven inside runComponent.
		if status != statusSkipped {
			return
		}
		if cr, ok := crByComponent[id]; ok {
			_ = w.svc.MarkComponentRun(ctx, a.OrgID, cr.ID, "skipped", "skipped (an upstream component did not pass)", "")
		}
	}

	final := schedule(ctx, nodes, concurrency, runFn, onState)

	msg := "workflow " + final
	_ = w.svc.MarkRun(ctx, a.OrgID, a.WorkflowRunID, final, msg)
	if final == runFailed {
		// Return an error so River retries the whole job; the retry is idempotent
		// (terminal components short-circuit, in-flight ones re-attach).
		return fmt.Errorf("workflows: run %s failed", a.WorkflowRunID)
	}
	return nil
}

// runComponent executes a single component to terminal and returns its scheduler
// outcome. It short-circuits a component already terminal in the DB (a retry must
// not re-run a succeeded component), marks it running, builds the RunSpec via the
// planner, executes the TaskRun crash-safely (re-attaching by the per-component
// label), captures logs + revisions, and marks the terminal status.
func (w *WorkflowRunWorker) runComponent(ctx context.Context, a WorkflowRunArgs, app *ent.Application, node GraphNode, cr *ent.ComponentRun) nodeResult {
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

	req, err := w.planComponent(ctx, app, node, a.Action, cr.RunName)
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
		_ = w.svc.MarkComponentRun(ctx, a.OrgID, cr.ID, "failed", err.Error(), runName)
		return nodeResult{Status: statusFailed, Err: err}
	}

	// Terminal: capture logs + resolved revisions best-effort before settling.
	logs := ""
	if w.captureLogs != nil {
		logs = w.captureLogs(ctx, runnerConn, runName)
	}
	if logs != "" {
		rev := helm.ParseRevisions(logs)
		_ = w.svc.SetComponentRunLogs(ctx, a.OrgID, cr.ID, logs, rev.Chart, "")
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
