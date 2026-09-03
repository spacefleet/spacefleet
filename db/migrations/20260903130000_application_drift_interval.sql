-- Scheduled drift checks: how often (in minutes) the worker starts a `drift`
-- run for the application's OpenTofu components. 0 (the default) means never;
-- the API accepts 0 or 15..10080 (a week). The scheduler skips an application
-- with a run already in flight and retries on its next tick.
ALTER TABLE applications ADD COLUMN drift_interval_minutes INTEGER NOT NULL DEFAULT 0;
