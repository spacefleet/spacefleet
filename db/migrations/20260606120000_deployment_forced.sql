-- Record whether a rollout run was a forced deploy: after the normal
-- `helm upgrade --install`, the release's workloads are restarted (the
-- equivalent of `kubectl rollout restart`) so pods cycle even when the chart
-- rendered no change. Stored on the run for the history view; only meaningful
-- for deploy/upgrade runs (an uninstall is never forced).

ALTER TABLE deployments
    ADD COLUMN forced BOOLEAN NOT NULL DEFAULT false;
