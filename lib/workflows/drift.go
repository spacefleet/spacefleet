package workflows

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/application"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
)

// driftTickInterval is how often the worker's drift scheduler looks for
// applications whose scheduled drift check is due. Cheap (one indexed query
// over applications with a schedule, one run lookup each), so it can run
// often; the granularity of a schedule is bounded below by this and by
// applications.MinDriftIntervalMinutes.
const driftTickInterval = 5 * time.Minute

// EnqueueRunFunc enqueues the executor job for a run that BeginRun opened,
// returning the job id to record on the run. It is injected by the worker
// process (which owns the River client) so this package needs no River
// dependency, exactly like LiveJobFunc for the reaper.
type EnqueueRunFunc func(ctx context.Context, args WorkflowRunArgs) (jobID string, err error)

// ScheduleDueDriftChecks starts a drift run for every application whose
// drift schedule is due: it has a non-zero interval, and its most recent
// drift run (of any status) started longer ago than that interval — or it
// has never had one. It scans across organizations (the worker is a single
// trusted process, not a per-tenant request path). Each check goes through
// the same path as a user-started run: BeginRun (which refuses while another
// run is in flight — such an application is simply retried next tick, since
// the gate is what keeps two runs off one state), then enqueue, then the job
// id is recorded. An application whose workflow has no OpenTofu component is
// skipped quietly; other failures are logged and the sweep moves on. Returns
// the number of runs started.
func (s *Service) ScheduleDueDriftChecks(ctx context.Context, enqueue EnqueueRunFunc) (int, error) {
	apps, err := s.ent.Application.Query().
		Where(application.DriftIntervalMinutesGT(0)).
		All(ctx)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	started := 0
	for _, app := range apps {
		due, err := s.driftDue(ctx, app, now)
		if err != nil {
			log.Printf("worker: drift scheduler: application %s: %v", app.ID, err)
			continue
		}
		if !due {
			continue
		}
		run, err := s.BeginRun(ctx, app.OrganizationID, app.ID, ActionDrift)
		if err != nil {
			switch {
			case errors.Is(err, ErrRunInFlight), errors.Is(err, ErrNoDriftTargets):
				// Busy, or nothing to check: not an error, just not now.
			default:
				log.Printf("worker: drift scheduler: application %s: begin run: %v", app.ID, err)
			}
			continue
		}
		jobID, err := enqueue(ctx, WorkflowRunArgs{
			WorkflowRunID: run.ID,
			OrgID:         app.OrganizationID,
			ApplicationID: app.ID,
			Action:        ActionDrift,
		})
		if err != nil {
			// The pending run arms the application's in-flight gate; never leave
			// it pending with no job behind it (see StartRun's identical posture).
			_ = s.MarkRun(ctx, app.OrganizationID, run.ID, "failed", "failed to enqueue scheduled drift check: "+err.Error())
			log.Printf("worker: drift scheduler: application %s: enqueue: %v", app.ID, err)
			continue
		}
		if err := s.SetRunJob(ctx, app.OrganizationID, run.ID, jobID); err != nil {
			log.Printf("worker: drift scheduler: application %s: record job id: %v", app.ID, err)
		}
		started++
	}
	return started, nil
}

// driftDue reports whether app's scheduled drift check is due at now: no
// drift run has ever been created for it, or the latest one was created more
// than its interval ago. It keys on created_at (not finished_at) so a check
// that is slow, or failed, still spaces the next one out by the interval.
func (s *Service) driftDue(ctx context.Context, app *ent.Application, now time.Time) (bool, error) {
	last, err := s.ent.WorkflowRun.Query().
		Where(
			workflowrun.OrganizationID(app.OrganizationID),
			workflowrun.ApplicationID(app.ID),
			workflowrun.ActionEQ(workflowrun.ActionDrift),
		).
		Order(ent.Desc(workflowrun.FieldCreatedAt)).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	interval := time.Duration(app.DriftIntervalMinutes) * time.Minute
	return now.Sub(last.CreatedAt) >= interval, nil
}

// RunDriftScheduler runs the scheduled-drift sweep on the worker process: an
// immediate sweep at startup, then once per driftTickInterval until ctx is
// cancelled. Each sweep that starts anything (or errors) is logged, so it is
// observable alongside the reaper and the heartbeat. Intended to be started
// in its own goroutine from the worker entrypoint.
func (s *Service) RunDriftScheduler(ctx context.Context, enqueue EnqueueRunFunc) {
	sweep := func() {
		n, err := s.ScheduleDueDriftChecks(ctx, enqueue)
		switch {
		case err != nil:
			log.Printf("worker: drift scheduler: %v", err)
		case n > 0:
			log.Printf("worker: drift scheduler: started %d scheduled drift check(s)", n)
		}
	}

	sweep()
	t := time.NewTicker(driftTickInterval)
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
