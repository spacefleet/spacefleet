package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	"github.com/spacefleet/spacefleet/lib/tofu"
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

// captureOutputsFn reads the captured-outputs key of a terraform pair's
// handover Secret from the runner connection — the worker side of the
// outputs channel a deploy apply step writes (see tofu.OutputsKey). A seam
// like runLogsFn, overridden in tests; nil bytes mean "nothing captured" (a
// missing Secret or key), an error is a read failure worth logging. Either
// way capture is best-effort and never fails a succeeded step.
type captureOutputsFn func(ctx context.Context, runnerConn k8s.Connection, namespace, secretName string) ([]byte, error)

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
	// the real pod-log read; captureOutputs to the real handover-Secret read.
	// Overridden in tests to drive Work without a cluster.
	funcs          tekton.RunFuncs
	captureLogs    runLogsFn
	captureOutputs captureOutputsFn
	// resolveOutputs reads a terraform apply unit's persisted outputs for the
	// ${{ components.* }} render context (this run first, then the latest
	// successful — svc.ResolveComponentOutputs in production); a seam so planner
	// unit tests run without a database.
	resolveOutputs func(ctx context.Context, orgID, runID, componentID uuid.UUID) (string, error)
	// ensureHandover / deleteHandover provision and sweep a terraform pair's
	// planfile-handover objects on the runner cluster (the real
	// tekton.EnsureHandoverSecret / DeleteHandoverSecret in production) — seams
	// for the same reason as funcs.
	ensureHandover func(ctx context.Context, conn k8s.Connection, namespace, name string, labels map[string]string) error
	deleteHandover func(ctx context.Context, conn k8s.Connection, namespace, name string) error
}

// NewWorker builds the workflow run worker over the workflow service and the
// shared run-input resolver (the same deps the applications service holds). It
// wires the real tekton executor and log capture; tests override the seams.
func NewWorker(svc *Service, resolver *deploy.Resolver) *WorkflowRunWorker {
	w := &WorkflowRunWorker{svc: svc, resolver: resolver, funcs: tekton.DefaultRunFuncs()}
	w.captureLogs = defaultCaptureLogs
	w.captureOutputs = defaultCaptureOutputs
	w.resolveOutputs = svc.ResolveComponentOutputs
	w.ensureHandover = tekton.EnsureHandoverSecret
	w.deleteHandover = tekton.DeleteHandoverSecret
	return w
}

// Timeout lets a workflow run watch outlive River's default job timeout.
func (w *WorkflowRunWorker) Timeout(*river.Job[WorkflowRunArgs]) time.Duration {
	return workflowWatchTimeout
}

// Work runs one workflow run end to end. It loads the run, its component runs, and
// the app; marks the run running; drives the DAG through schedule(); then marks
// the run with the scheduler's terminal status. A failure with a retryable cause
// returns an error WITHOUT settling the run, so River retries the job against a
// still-in-flight run (H3) — retries are safe because already-terminal components
// short-circuit and in-flight TaskRuns re-attach (never double-submit) via the
// per-component label. Everything else settles the run terminal and returns nil
// (succeeded/partial, and failures that won't change on retry).
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

	// attemptsRemain reports whether River will actually run this job again if Work
	// returns an error — Attempt is the 1-based number of the executing attempt, so
	// the final attempt has Attempt == MaxAttempts. It decides whether a retryable
	// failure may leave the run and its components in-flight for the retry to
	// re-attach (H3), or must settle them terminal because no retry will follow.
	attemptsRemain := job.Attempt < job.MaxAttempts

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
		res := w.runComponent(ctx, a, app, node, cr, nodeByID, attemptsRemain)
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
			// H3: while a retryable failure is pending a River retry, don't persist the
			// skip — it may sit downstream of the retryably-failed node, and a terminal
			// "skipped" would short-circuit it on the retried attempt even when the node
			// it waited on then succeeds. Leaving it pending is safe: a skip implies a
			// hard failure somewhere, so a retryable run always returns the retry error
			// (re-driving the DAG re-settles it), and a run that instead settles terminal
			// persists it via the SettleStuckComponentRuns sweep below.
			ranMu.Lock()
			deferred := attemptsRemain && retryable
			ranMu.Unlock()
			if deferred {
				return
			}
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

	// F4/H3: a failure that River will retry must NOT settle the run — MarkRun is
	// guarded to never resurrect a terminal run, so marking it failed here would
	// freeze it at "failed" forever (the retry's MarkRun(running) becomes a no-op)
	// even when the retried attempt succeeds on the cluster. So when at least one
	// component failed for a genuinely retryable reason — a transient/infra
	// condition where re-running can change the outcome (e.g. the TaskRun couldn't
	// be submitted or watched), or a context cancellation/timeout — and an attempt
	// remains, return the error with the run still in-flight: the retry is
	// idempotent (already-terminal components short-circuit, in-flight ones
	// re-attach via the per-component label), so only the unsettled work re-runs.
	if final == runFailed && attemptsRemain {
		if ctx.Err() != nil {
			return fmt.Errorf("workflows: run %s aborted: %w", a.WorkflowRunID, ctx.Err())
		}
		if retryable {
			return fmt.Errorf("workflows: run %s failed (retryable): %w", a.WorkflowRunID, retryableErr)
		}
	}

	// Terminal: the run settles here. Either it didn't fail at all, the failure is
	// fully attributable to settled component results that won't change on retry —
	// a TaskRun that ran to a terminal Failed phase, or a deterministic plan/config
	// error (retrying would only re-run the same failing scripts ~25 times) — or
	// this was the final attempt and no retry will follow. The terminal bookkeeping
	// uses a cancel-immune context so a timed-out final attempt still settles
	// instead of lingering for the reaper; both writes are status-guarded, so they
	// can't clobber a run CancelRun already settled.
	markCtx := context.WithoutCancel(ctx)
	_ = w.svc.MarkRun(markCtx, a.OrgID, a.WorkflowRunID, final, "workflow "+final)

	// Settle any component run the scheduler left non-terminal. The cases that bite:
	// a gated node that parked at awaiting_approval on a branch while a parallel
	// branch hard-failed (hardFailed outranks suspended, so schedule() returns
	// runFailed/runPartial and the parked node is never settled), and the skips the
	// retryable path above deferred on earlier attempts. The run is now terminal, so
	// a non-terminal step would otherwise sit pending/awaiting_approval forever with
	// no recovery path (approve/reject and cancel all require an in-flight run).
	// Sweep those to skipped, mirroring CancelRun's settle-steps. Terminal steps are
	// untouched, so genuine succeeded/failed/skipped results are preserved.
	if final == runFailed || final == runPartial {
		_, _ = w.svc.SettleStuckComponentRuns(markCtx, a.OrgID, a.WorkflowRunID, "skipped (run did not complete)")
	}

	// The run is terminal, so no apply node will read a planfile-handover Secret
	// again — sweep the run's handover Secrets (their RBAC rides along via
	// ownerReferences). A succeeded apply already deleted its own, so this
	// catches the unhappy paths: a failed plan, a rejected approval (the resume
	// job settles the run here), a skipped apply, and a tolerated in-step delete
	// failure. Cancel- and reaper-settled runs never pass through here and leak
	// until an operator sweeps by the stamped labels — the same posture as their
	// TaskRuns. Best-effort on markCtx, like the bookkeeping above.
	w.sweepPlanArtifacts(markCtx, a, app, snapshot)

	if final == runFailed {
		// Retries exhausted on a retryable/aborted failure: the run is settled above;
		// still return the error so River records why on the job row.
		if ctx.Err() != nil {
			return fmt.Errorf("workflows: run %s aborted: %w", a.WorkflowRunID, ctx.Err())
		}
		if retryable {
			return fmt.Errorf("workflows: run %s failed (retryable, retries exhausted): %w", a.WorkflowRunID, retryableErr)
		}
	}
	return nil
}

// runComponent executes a single component to terminal and returns its scheduler
// outcome. It short-circuits a component already terminal in the DB (a retry must
// not re-run a succeeded component), marks it running, builds the RunSpec via the
// planner, executes the TaskRun crash-safely (re-attaching by the per-component
// label), captures logs + revisions, and marks the terminal status. attemptsRemain
// (whether River will retry the job after a returned error) decides whether a
// retryable executor failure may leave the component run in-flight for the retry
// to pick up (H3), or must settle it failed because no retry will follow.
func (w *WorkflowRunWorker) runComponent(ctx context.Context, a WorkflowRunArgs, app *ent.Application, node GraphNode, cr *ent.ComponentRun, byID map[uuid.UUID]GraphNode, attemptsRemain bool) nodeResult {
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
		// component result. Mark it retryable so the run's River job retries (F4).
		// While a retry remains, the component run must stay in-flight (H3): a
		// terminal "failed" here would make the retry's short-circuit above return
		// failed without ever re-attaching, freezing the step (and the run) at failed
		// even when the TaskRun is still running — or succeeding — on the cluster. So
		// record the transient error on the still-running row and let the next attempt
		// re-attach via the per-component recovery label. Only when no retry will
		// follow is the failure settled terminal.
		if attemptsRemain {
			_ = w.svc.MarkComponentRun(ctx, a.OrgID, cr.ID, "running", "transient failure, retrying: "+err.Error(), runName)
		} else {
			_ = w.svc.MarkComponentRun(ctx, a.OrgID, cr.ID, "failed", err.Error(), runName)
		}
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
	// A terraform apply unit that succeeded on a deploy run handed the module's
	// `tofu output -json` back through the pair's handover Secret — read and
	// persist it before settling the step (so the settled row already carries
	// its outputs), then delete the spent Secret. Best-effort throughout: a
	// capture failure never fails a step whose apply succeeded.
	if a.Action == ActionDeploy && node.Type == TypeTerraform && node.Config[terraformConfigCommand] == terraformCommandApply {
		w.captureTofuOutputs(ctx, a, node, byID, runnerConn, cr.ID)
	}
	_ = w.svc.MarkComponentRun(ctx, a.OrgID, cr.ID, "succeeded", finalStatus.Message, runName)
	return nodeResult{Status: statusSucceeded}
}

// captureTofuOutputs reads the outputs a terraform apply unit stored in its
// pair's handover Secret, persists them on the unit's component_run row, and
// deletes the Secret (its planfile is spent and its outputs are now durable).
// Every failure is logged and swallowed — the apply already succeeded, so
// missing outputs degrade the record, never the run — and a Secret left behind
// by a failure here is still deleted by the terminal sweep.
func (w *WorkflowRunWorker) captureTofuOutputs(ctx context.Context, a WorkflowRunArgs, node GraphNode, byID map[uuid.UUID]GraphNode, runnerConn k8s.Connection, crID uuid.UUID) {
	// The Secret is keyed off the upstream plan node, exactly as the planner
	// named it; no plan node (defensive — the apply script fails closed there)
	// means no Secret to read.
	planID := upstreamTofuPlanID(node, byID)
	if planID == uuid.Nil {
		return
	}
	name := tofuPlanArtifactSecret(a.WorkflowRunID, planID)
	raw, err := w.captureOutputs(ctx, runnerConn, tekton.JobsNamespace, name)
	if err != nil {
		log.Printf("worker: workflow run %s: read outputs from secret %s: %v", a.WorkflowRunID, name, err)
		return
	}
	outputs, err := normalizeTofuOutputs(raw)
	if err != nil {
		log.Printf("worker: workflow run %s: parse outputs from secret %s: %v", a.WorkflowRunID, name, err)
		return
	}
	if outputs != "" {
		if err := w.svc.SetComponentRunOutputs(ctx, a.OrgID, crID, outputs); err != nil {
			// Keep the Secret: the outputs aren't durable yet, and the terminal
			// sweep will delete it regardless.
			log.Printf("worker: workflow run %s: persist outputs for component run %s: %v", a.WorkflowRunID, crID, err)
			return
		}
	}
	if err := w.deleteHandover(ctx, runnerConn, tekton.JobsNamespace, name); err != nil {
		log.Printf("worker: workflow run %s: delete outputs secret %s: %v", a.WorkflowRunID, name, err)
	}
}

// tofuOutput mirrors one entry of `tofu output -json`: the value and its type
// as raw JSON (so non-string values survive a round-trip untouched) plus the
// module author's sensitive flag, which drives the below-editor redaction in
// the API layer.
type tofuOutput struct {
	Value     json.RawMessage `json:"value"`
	Type      json.RawMessage `json:"type"`
	Sensitive bool            `json:"sensitive"`
}

// normalizeTofuOutputs validates and canonicalizes the raw `tofu output -json`
// bytes read from the handover Secret into the JSON object persisted on
// component_runs.outputs. Empty input or an empty object yields "" (a module
// with no outputs is the common case — the column stays empty); bytes that
// don't parse as tofu's shape are an error for the caller to log.
func normalizeTofuOutputs(raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var outputs map[string]tofuOutput
	if err := json.Unmarshal(raw, &outputs); err != nil {
		return "", err
	}
	if len(outputs) == 0 {
		return "", nil
	}
	b, err := json.Marshal(outputs)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// defaultCaptureOutputs reads the tofu.OutputsKey entry of a terraform pair's
// handover Secret from the runner connection — the production seam behind
// w.captureOutputs.
func defaultCaptureOutputs(ctx context.Context, runnerConn k8s.Connection, namespace, secretName string) ([]byte, error) {
	return tekton.ReadHandoverSecretKey(ctx, runnerConn, namespace, secretName, tofu.OutputsKey)
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

// sweepPlanArtifacts best-effort deletes every planfile-handover Secret a
// terminal run may have left on the runner cluster — one per terraform plan
// node in the snapshot, named exactly as the planner keyed them. Deleting the
// Secret garbage-collects the pair's ServiceAccount/Role/RoleBinding through
// their ownerReferences; a Secret the apply step already deleted is a no-op.
// Failures are logged, never propagated: the run is already settled, and a
// leftover Secret is inert (its names are per-run, so nothing ever reads it
// again).
func (w *WorkflowRunWorker) sweepPlanArtifacts(ctx context.Context, a WorkflowRunArgs, app *ent.Application, snapshot GraphSnapshot) {
	// A preview neither pre-creates handover Secrets nor stores planfiles.
	if a.Action == ActionPreview {
		return
	}
	var names []string
	for _, n := range snapshot.Nodes {
		if n.Type == TypeTerraform && n.Config[terraformConfigCommand] == terraformCommandPlan {
			names = append(names, tofuPlanArtifactSecret(a.WorkflowRunID, n.ID))
		}
	}
	if len(names) == 0 {
		return
	}
	conn, err := w.resolver.RunnerConn(ctx, a.OrgID, app.RunnerClusterID)
	if err != nil {
		log.Printf("worker: workflow run %s: resolve runner for planfile sweep: %v", a.WorkflowRunID, err)
		return
	}
	for _, name := range names {
		if err := w.deleteHandover(ctx, conn, tekton.JobsNamespace, name); err != nil {
			log.Printf("worker: workflow run %s: sweep planfile secret %s: %v", a.WorkflowRunID, name, err)
		}
	}
}

// defaultCaptureLogs reads a terminal run's full pod logs from the runner
// connection, capped, no follow — best-effort, "" on any failure. Mirrors
// applications.fetchRunLogs but works straight off the resolved runner connection
// (the planner already resolved it), so the worker needs no extra cluster lookup.
func defaultCaptureLogs(ctx context.Context, runnerConn k8s.Connection, runName string) string {
	if runName == "" {
		return ""
	}
	run, err := tekton.GetRun(ctx, runnerConn, tekton.JobsNamespace, runName)
	if err != nil || run.PodName == "" {
		return ""
	}
	rc, err := k8s.StreamPodLogs(ctx, runnerConn, tekton.JobsNamespace, run.PodName, k8s.LogOptions{Follow: false})
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
