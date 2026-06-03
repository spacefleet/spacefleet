-- Applications: deployable workloads registered to an organization. The first
-- type is a Helm release. config + chart_source say where the chart comes from;
-- values is the raw values.yaml override; target_cluster_id + target_namespace
-- are where the release is installed; runner_cluster_id is the Tekton-enabled
-- cluster the `helm upgrade --install` job runs on. status is the rollout
-- lifecycle (pending → deploying → deployed/failed, plus uninstalling →
-- uninstalled); job_id correlates the in-flight rollout job; last_run_name is
-- the TaskRun on the runner for the most recent rollout (for streaming).
--
-- The cluster FKs are ON DELETE RESTRICT: an app blocks deletion of a cluster it
-- targets or runs on. The org FK is ON DELETE CASCADE (the app disappears with
-- its organization).

CREATE TABLE applications (
    id                UUID PRIMARY KEY,
    organization_id   UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    type              TEXT NOT NULL DEFAULT 'helm',
    chart_source      TEXT NOT NULL,
    config            JSONB NOT NULL DEFAULT '{}'::jsonb,
    "values"          TEXT NOT NULL DEFAULT '',
    release_name      TEXT NOT NULL DEFAULT '',
    target_namespace  TEXT NOT NULL,
    target_cluster_id UUID NOT NULL REFERENCES clusters(id) ON DELETE RESTRICT,
    runner_cluster_id UUID NOT NULL REFERENCES clusters(id) ON DELETE RESTRICT,
    status            TEXT NOT NULL DEFAULT 'pending',
    status_message    TEXT NOT NULL DEFAULT '',
    job_id            TEXT NOT NULL DEFAULT '',
    last_run_name     TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL
);

CREATE INDEX applications_organization_id ON applications (organization_id);
CREATE UNIQUE INDEX applications_organization_id_name ON applications (organization_id, name);
CREATE INDEX applications_target_cluster_id ON applications (target_cluster_id);
CREATE INDEX applications_runner_cluster_id ON applications (runner_cluster_id);
