-- Record the git commit SHAs a rollout resolved. Git-sourced charts and values
-- are pulled on deploy from a mutable ref (a branch can move between runs), so
-- the rollout script echoes the resolved SHA of each clone and the worker stores
-- it here at terminal — making a past run auditable and reproducible. Both are
-- empty for sources that aren't a git clone (an http_repo/oci chart, or a run
-- with no values-from-git source).

ALTER TABLE deployments
    ADD COLUMN chart_revision  TEXT NOT NULL DEFAULT '',
    ADD COLUMN values_revision TEXT NOT NULL DEFAULT '';
