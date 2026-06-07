package workflows

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/componentrun"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
)

// ErrRunNotInFlight is returned by CancelRun when the run is already terminal
// (succeeded/failed/partial) — there is nothing in flight to cancel. A handler
// maps it to 409.
var ErrRunNotInFlight = errors.New("workflows: run is not in flight (already settled)")

// reapMaxLifetime is how long a run may sit in running before the reaper treats
// it as abandoned. It must exceed the worker's own watch timeout
// (workflowWatchTimeout) so the reaper only fires after the River job that owns
// the run has itself given up — a run still inside its timeout is the worker's to
// finish, not the reaper's to steal. The headroom past the timeout absorbs clock
// skew and the worker's own terminal-marking latency.
const reapMaxLifetime = workflowWatchTimeout + 15*time.Minute

// reapInterval is the cadence the worker's startup sweep re-runs at. Cheap (one
// indexed query plus a River JobGet per stuck run, and there are rarely any), so
// a few minutes keeps abandoned runs from lingering without busy-polling.
const reapInterval = 5 * time.Minute

// CancelRun cancels an in-flight workflow run (pending, running, or parked
// awaiting_approval): it verifies the run belongs to the app and org, refuses a run
// that has already settled (ErrRunNotInFlight → 409),
// then — in one transaction — marks the run failed and settles every non-terminal
// component run failed. Marking the run terminal clears the application's
// in-flight gate (BeginRun's pending/running guard), so a new run can start.
//
// The DB write is only half of a cancel: the caller (the handler) also cancels the
// run's River job, which stops a worker that is actively executing the run mid-DAG
// (and prevents a retry). This DB write is what makes the cancel durable even when
// there is no live worker to stop (a pending job, or a job already gone): the
// settle is guarded by a status predicate inside the tx, so it is a no-op if the
// run settled concurrently (a second cancel, or the worker finishing first), and
// MarkRun is likewise guarded so the worker's terminal write can't resurrect a
// cancelled run. Org-scoped throughout; a run not in the org/app surfaces as ent's
// NotFoundError (→ 404).
func (s *Service) CancelRun(ctx context.Context, orgID, appID, runID uuid.UUID) (*ent.WorkflowRun, error) {
	run, err := s.ent.WorkflowRun.Query().
		Where(
			workflowrun.OrganizationID(orgID),
			workflowrun.ApplicationID(appID),
			workflowrun.ID(runID),
		).
		Only(ctx)
	if err != nil {
		return nil, err
	}
	if run.Status != workflowrun.StatusPending &&
		run.Status != workflowrun.StatusRunning &&
		run.Status != workflowrun.StatusAwaitingApproval {
		return nil, ErrRunNotInFlight
	}

	tx, err := s.ent.Tx(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	// Conditional settle: re-assert the in-flight status as a predicate inside the
	// tx so a run that the worker (or a concurrent cancel) settled between the read
	// above and here is not double-settled. Zero rows affected means we lost that
	// race — the run is already terminal, so this is a no-op cancel.
	affected, err := tx.WorkflowRun.Update().
		Where(
			workflowrun.OrganizationID(orgID),
			workflowrun.ID(runID),
			workflowrun.StatusIn(workflowrun.StatusPending, workflowrun.StatusRunning, workflowrun.StatusAwaitingApproval),
		).
		SetStatus(workflowrun.StatusFailed).
		SetMessage("run cancelled").
		SetFinishedAt(now).
		Save(ctx)
	if err != nil {
		return nil, rollback(tx, err)
	}
	if affected == 0 {
		return nil, rollback(tx, ErrRunNotInFlight)
	}

	// Settle every still-running/pending component run so the run's steps reflect
	// the cancel and don't sit pending forever. Terminal steps are left untouched.
	if err := tx.ComponentRun.Update().
		Where(
			componentrun.OrganizationID(orgID),
			componentrun.WorkflowRunID(runID),
			componentrun.StatusIn(componentrun.StatusPending, componentrun.StatusRunning, componentrun.StatusAwaitingApproval),
		).
		SetStatus(componentrun.StatusFailed).
		SetMessage("cancelled (run cancelled)").
		SetFinishedAt(now).
		Exec(ctx); err != nil {
		return nil, rollback(tx, err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// Re-read the settled run to return the current row (the conditional Update
	// above doesn't hand back the entity).
	return s.ent.WorkflowRun.Query().
		Where(workflowrun.OrganizationID(orgID), workflowrun.ID(runID)).
		Only(ctx)
}

// LiveJobFunc reports whether the River job with the given id is still live —
// i.e. available, scheduled, running, retryable, or pending (anything that could
// still settle the run). It is injected by the worker process (which owns the
// River client) so this package needs no River dependency. A nil func makes the
// reaper conservative: it never reaps (it can't tell a stuck run from a live one),
// so it only ever fails runs when it can positively confirm the job is gone.
type LiveJobFunc func(ctx context.Context, jobID string) (bool, error)

// ReapStuckRuns fails workflow runs that have been stuck in running past
// reapMaxLifetime with no live River job behind them. This is the safety net for
// the one case the worker's own panic recovery can't cover: a hard kill (SIGKILL,
// node loss, OOM) that unwinds the process without running Work's deferred
// mark-failed, leaving the run row "running" forever and wedging the app's
// in-flight gate.
//
// It scans across organizations (the worker is a single trusted process, not a
// per-tenant request path) for running runs older than the cutoff, and for each
// asks liveJob whether the run's River job is still around. Only a run whose job
// is positively gone is marked failed — if liveJob errors or is nil we leave the
// run alone, erring toward never stealing a run a worker is still finishing.
// Returns the number of runs reaped.
func (s *Service) ReapStuckRuns(ctx context.Context, maxLifetime time.Duration, liveJob LiveJobFunc) (int, error) {
	cutoff := time.Now().Add(-maxLifetime)
	stuck, err := s.ent.WorkflowRun.Query().
		Where(
			workflowrun.StatusEQ(workflowrun.StatusRunning),
			workflowrun.StartedAtLT(cutoff),
		).
		All(ctx)
	if err != nil {
		return 0, err
	}

	reaped := 0
	for _, run := range stuck {
		// Conservative: a run with no recorded job id, or one we can't check, is
		// left for the worker — we only reap when we can confirm the job is gone.
		if liveJob == nil || run.JobID == "" {
			continue
		}
		live, err := liveJob(ctx, run.JobID)
		if err != nil || live {
			continue
		}
		if err := s.MarkRun(ctx, run.OrganizationID, run.ID, "failed", "run abandoned: no live worker and exceeded max lifetime"); err != nil {
			// Don't abort the sweep over one row; the next tick retries it.
			continue
		}
		// Settle any non-terminal component runs too, so the reaped run's steps
		// don't linger pending/running. Best-effort per the sweep contract.
		_, _ = s.ent.ComponentRun.Update().
			Where(
				componentrun.OrganizationID(run.OrganizationID),
				componentrun.WorkflowRunID(run.ID),
				componentrun.StatusIn(componentrun.StatusPending, componentrun.StatusRunning),
			).
			SetStatus(componentrun.StatusFailed).
			SetMessage("abandoned (run reaped)").
			SetFinishedAt(time.Now()).
			Save(ctx)
		reaped++
	}
	return reaped, nil
}

// RunReaper runs the stuck-run sweep on the worker process: an immediate sweep at
// startup (so a run abandoned while the worker was down is settled promptly on the
// next boot) then once per reapInterval until ctx is cancelled. It uses the
// package's reapMaxLifetime cutoff and logs each sweep that reaps anything (and
// any sweep error), so it's observable in the worker's log stream alongside the
// heartbeat. Intended to be started in its own goroutine from the worker
// entrypoint.
func (s *Service) RunReaper(ctx context.Context, liveJob LiveJobFunc) {
	sweep := func() {
		n, err := s.ReapStuckRuns(ctx, reapMaxLifetime, liveJob)
		switch {
		case err != nil:
			log.Printf("worker: workflow run reaper: %v", err)
		case n > 0:
			log.Printf("worker: workflow run reaper: failed %d abandoned run(s)", n)
		}
	}

	sweep()
	t := time.NewTicker(reapInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}
