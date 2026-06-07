package queue

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// Client wraps a *river.Client[pgx.Tx]. We keep the wrapper thin so we
// can swap drivers later (River's roadmap mentions database/sql support)
// without touching every caller.
//
// Client is safe for concurrent use; River's own client is.
type Client struct {
	inner *river.Client[pgx.Tx]
	mode  bool
}

// Insert enqueues a job using the client's pool. Wraps River's Insert
// so callers don't have to import river or rivertype.
func (c *Client) Insert(ctx context.Context, args river.JobArgs) (*rivertype.JobInsertResult, error) {
	if c == nil {
		return nil, errors.New("queue: nil client")
	}
	res, err := c.inner.Insert(ctx, args, nil)
	if err != nil {
		return nil, fmt.Errorf("queue: insert %s: %w", args.Kind(), err)
	}
	return res, nil
}

// JobLive reports whether the job with the given id is still live — in a state
// that could still settle (available, scheduled, running, retryable, pending) —
// as opposed to gone (completed, cancelled, discarded, or no longer in the
// table). It's the reaper's "is a worker still on this?" probe: River's ErrNotFound
// means the job is gone (returns false, nil); any other error is surfaced so the
// caller can stay conservative and not reap on a transient DB hiccup.
func (c *Client) JobLive(ctx context.Context, id int64) (bool, error) {
	if c == nil {
		return false, errors.New("queue: nil client")
	}
	row, err := c.inner.JobGet(ctx, id)
	if err != nil {
		if errors.Is(err, river.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("queue: job get %d: %w", id, err)
	}
	switch row.State {
	case rivertype.JobStateAvailable,
		rivertype.JobStateScheduled,
		rivertype.JobStateRunning,
		rivertype.JobStateRetryable,
		rivertype.JobStatePending:
		return true, nil
	default:
		// Completed, cancelled, discarded — the job will never settle the run.
		return false, nil
	}
}

// JobCancel cancels the job with the given id: River marks it cancelled (a
// terminal state, so it will not be retried) and, if an attempt is currently
// running — possibly in another process — cancels that attempt's context via
// River's notifier so the worker stops promptly. A job that is already gone or
// finished (ErrNotFound) is treated as a no-op. It's the run-cancel path's way to
// actually stop an in-flight workflow run, not just rewrite its DB rows.
func (c *Client) JobCancel(ctx context.Context, id int64) error {
	if c == nil {
		return errors.New("queue: nil client")
	}
	if _, err := c.inner.JobCancel(ctx, id); err != nil {
		if errors.Is(err, river.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("queue: job cancel %d: %w", id, err)
	}
	return nil
}

// Start begins fetching and working jobs. Returns an error if the
// client wasn't built with WorkerMode. The caller's ctx is what River
// passes to each Worker.Work — cancel ctx to drain.
func (c *Client) Start(ctx context.Context) error {
	if !c.mode {
		return errors.New("queue: insert-only client cannot Start")
	}
	return c.inner.Start(ctx)
}

// Stop blocks until in-flight jobs settle or ctx fires. Mirrors
// river.Client.Stop. Safe to call on insert-only clients (no-op).
func (c *Client) Stop(ctx context.Context) error {
	if !c.mode {
		return nil
	}
	return c.inner.Stop(ctx)
}
