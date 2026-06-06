-- Sync (preview/diff) state for applications, kept strictly disjoint from the
-- rollout columns (status/status_message/job_id/last_run_name) so a refresh —
-- which runs `helm diff` as a TaskRun and changes nothing — never clobbers an
-- in-flight rollout, and vice versa. A refresh re-resolves the desired state
-- (latest git SHA / chart version / values) and diffs it against the live
-- cluster; the result is cached here for the deploy-confirmation UI.

ALTER TABLE applications
    ADD COLUMN sync_status             TEXT        NOT NULL DEFAULT 'unknown',
    ADD COLUMN sync_message            TEXT        NOT NULL DEFAULT '',
    ADD COLUMN last_diff               TEXT        NOT NULL DEFAULT '',
    ADD COLUMN desired_chart_revision  TEXT        NOT NULL DEFAULT '',
    ADD COLUMN desired_values_revision TEXT        NOT NULL DEFAULT '',
    ADD COLUMN last_refreshed_at       TIMESTAMPTZ,
    ADD COLUMN sync_job_id             TEXT        NOT NULL DEFAULT '',
    ADD COLUMN sync_run_name           TEXT        NOT NULL DEFAULT '';
