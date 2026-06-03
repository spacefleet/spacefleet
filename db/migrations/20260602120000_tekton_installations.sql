-- Tekton installations: marks a cluster as a place that can run jobs (CI/CD,
-- Helm releases, …) backed by Tekton, and tracks the lifecycle of installing
-- Tekton into it. 1:1 with a cluster (CASCADE so it disappears with the
-- cluster). status is the install lifecycle (not_installed → installing →
-- installed/failed, plus upgrading/uninstalling); job_id correlates the
-- in-flight install job; installed_version is the pinned release last applied.

CREATE TABLE tekton_installations (
    id                UUID PRIMARY KEY,
    cluster_id        UUID NOT NULL UNIQUE REFERENCES clusters(id) ON DELETE CASCADE,
    enabled           BOOLEAN NOT NULL DEFAULT false,
    status            TEXT NOT NULL DEFAULT 'not_installed',
    installed_version TEXT NOT NULL DEFAULT '',
    status_message    TEXT NOT NULL DEFAULT '',
    job_id            TEXT NOT NULL DEFAULT '',
    last_checked_at   TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL
);

CREATE UNIQUE INDEX tekton_installations_cluster_id ON tekton_installations (cluster_id);
