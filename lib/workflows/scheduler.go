package workflows

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// Scheduler outcome statuses. They mirror the ComponentRun status enum (minus
// pending, which is the initial DB state) and the WorkflowRun terminal statuses,
// but the scheduler is a pure orchestrator — it imports no ent/tekton/k8s and
// only ever deals in these strings, so it is fully unit-testable (and race-tested)
// on its own.
const (
	statusRunning   = "running"
	statusSucceeded = "succeeded"
	statusFailed    = "failed"
	statusSkipped   = "skipped"

	// Run terminal statuses returned by schedule.
	runSucceeded = "succeeded"
	runFailed    = "failed"
	runPartial   = "partial"
)

// schedNode is one node of the DAG as the scheduler sees it: an id, the ids it
// depends on, and whether a failure of *this* node is tolerated (continue-on-
// failure). All the type-specific detail (config, targeting, credentials) is
// resolved by runFn against the caller's snapshot, not here.
type schedNode struct {
	ID                uuid.UUID
	DependsOn         []uuid.UUID
	ContinueOnFailure bool
}

// nodeResult is the outcome of running one node to terminal. Status is one of
// statusSucceeded / statusFailed / statusSkipped; Err carries the failure detail
// (for the caller's logging) and is otherwise nil. Retryable distinguishes a
// failure the caller can usefully retry (an infra/transient condition — e.g. the
// TaskRun couldn't be submitted or watched) from a permanent one (the component
// ran to a terminal Failed phase, so retrying re-runs the same failing script).
// The scheduler itself ignores Retryable; it's consumed by the worker to decide
// whether to return an error from Work (retry the River job) or nil (settle the
// run failed without burning retries — F4).
type nodeResult struct {
	Status    string
	Err       error
	Retryable bool
}

// schedule runs a DAG of nodes to completion and returns the run's terminal
// status. It is pure: it owns only the readiness/skip bookkeeping and the bounded
// concurrency, delegating each node's actual execution to runFn and each state
// transition to onState. It imports nothing from ent/tekton/k8s.
//
// Semantics (matching the plan exactly):
//   - A node becomes RUNNABLE when every dep has "passed": a dep passed if it
//     succeeded, OR it failed and that dep's ContinueOnFailure is true.
//   - A node is SKIPPED (without running) if any dep hard-failed (failed &&
//     !ContinueOnFailure) or was itself skipped. Skips propagate transitively.
//   - All currently-runnable nodes run CONCURRENTLY behind a bounded semaphore of
//     size concurrency (>=1). As each finishes, readiness is re-evaluated.
//   - onState(id, "running"/"succeeded"/"failed"/"skipped") fires on each
//     transition so the caller can persist + drive the SSE stream.
//   - Final status: "failed" if any node hard-failed; else "partial" if any node
//     failed (continue-on-failure); else "succeeded". An empty graph → "succeeded".
//
// Shared state is guarded by a single mutex; runFn runs outside the lock so node
// executions are genuinely concurrent. `go test -race` covers this.
func schedule(
	ctx context.Context,
	nodes []schedNode,
	concurrency int,
	runFn func(ctx context.Context, n schedNode) nodeResult,
	onState func(id uuid.UUID, status string),
) string {
	if len(nodes) == 0 {
		return runSucceeded
	}
	if concurrency < 1 {
		concurrency = 1
	}

	contOnFail := make(map[uuid.UUID]bool, len(nodes))
	known := make(map[uuid.UUID]struct{}, len(nodes))
	for _, n := range nodes {
		contOnFail[n.ID] = n.ContinueOnFailure
		known[n.ID] = struct{}{}
	}

	var (
		mu       sync.Mutex
		results  = make(map[uuid.UUID]string, len(nodes)) // terminal status per settled node
		inflight = make(map[uuid.UUID]bool, len(nodes))   // running or scheduled-to-run
		done     = make(chan uuid.UUID)                   // ids of just-settled (executed) nodes
		sem      = make(chan struct{}, concurrency)
		active   int // goroutines that will eventually push to done
	)

	emit := func(id uuid.UUID, status string) {
		if onState != nil {
			onState(id, status)
		}
	}

	// depPassed reports whether a settled dependency lets its dependents proceed:
	// succeeded, or failed-with-continue-on-failure. Caller holds mu.
	depPassed := func(dep uuid.UUID) bool {
		switch results[dep] {
		case statusSucceeded:
			return true
		case statusFailed:
			return contOnFail[dep]
		default:
			return false
		}
	}
	// depBlocks reports whether a settled dependency forces its dependents to skip:
	// a hard failure (failed && !continue-on-failure) or itself skipped. Caller
	// holds mu.
	depBlocks := func(dep uuid.UUID) bool {
		switch results[dep] {
		case statusFailed:
			return !contOnFail[dep]
		case statusSkipped:
			return true
		default:
			return false
		}
	}

	// markSettled records a terminal status and fires onState. Caller holds mu.
	markSettled := func(id uuid.UUID, status string) {
		results[id] = status
		delete(inflight, id)
		emit(id, status)
	}

	// launch runs one node concurrently: emit "running", run runFn outside the lock
	// (behind the semaphore), record the result, then push the id onto done so the
	// main loop re-sweeps. Declared before sweep, which calls it.
	launch := func(n schedNode) {
		go func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			emit(n.ID, statusRunning)
			res := runFn(ctx, n)
			status := res.Status
			switch status {
			case statusSucceeded, statusFailed, statusSkipped:
			default:
				// A runFn returning an unrecognized status is treated as a failure so the
				// run can't silently mis-settle.
				status = statusFailed
			}
			mu.Lock()
			markSettled(n.ID, status)
			mu.Unlock()
			done <- n.ID
		}()
	}

	// sweep evaluates every not-yet-settled, not-inflight node: skip it if a
	// dependency blocks; otherwise launch it once all deps have passed. Returns how
	// many nodes it launched (so the loop tracks outstanding goroutines). It loops
	// to a fixpoint so a skip can transitively skip dependents in the same pass.
	// Caller holds mu.
	sweep := func() int {
		launched := 0
		for {
			progressed := false
			for _, n := range nodes {
				if _, settled := results[n.ID]; settled {
					continue
				}
				if inflight[n.ID] {
					continue
				}
				blocked := false
				ready := true
				unknownDep := false
				for _, dep := range n.DependsOn {
					if _, ok := known[dep]; !ok {
						// A dep id not present in the node set can never settle, so the
						// dependent would otherwise hang forever (and the run could report
						// succeeded while this component stays pending). Treat it as a hard
						// error: fail the dependent now. validateDAG rejects unknown deps at
						// write time, so this only fires on a corrupt/edited snapshot — but
						// the scheduler must not strand a node on it.
						unknownDep = true
						break
					}
					if _, settled := results[dep]; !settled {
						ready = false // a dep not yet settled neither blocks nor passes
						continue
					}
					if depBlocks(dep) {
						blocked = true
						break
					}
					if !depPassed(dep) {
						ready = false
					}
				}
				if unknownDep {
					markSettled(n.ID, statusFailed)
					progressed = true
					continue
				}
				if blocked {
					markSettled(n.ID, statusSkipped)
					progressed = true
					continue
				}
				if ready {
					inflight[n.ID] = true
					launched++
					launch(n)
				}
			}
			if !progressed {
				return launched
			}
		}
	}

	mu.Lock()
	active += sweep()
	pending := active
	mu.Unlock()

	for pending > 0 {
		select {
		case <-ctx.Done():
			// The context is cancelled (e.g. the job's timeout). Drain the outstanding
			// goroutines so none leak, then report failed so River retries the job.
			for pending > 0 {
				<-done
				pending--
			}
			return runFailed
		case <-done:
			mu.Lock()
			launched := sweep()
			mu.Unlock()
			pending = pending - 1 + launched
		}
	}

	mu.Lock()
	defer mu.Unlock()
	hardFailed, softFailed := false, false
	for _, n := range nodes {
		if results[n.ID] == statusFailed {
			if contOnFail[n.ID] {
				softFailed = true
			} else {
				hardFailed = true
			}
		}
	}
	switch {
	case hardFailed:
		return runFailed
	case softFailed:
		return runPartial
	default:
		return runSucceeded
	}
}
