-- Deployments: one row per application rollout (a deploy/upgrade/uninstall
-- attempt), giving an application a CI-like history of its runs. The application
-- row tracks only the current rollout (status + last_run_name); a deployment is
-- the durable per-run record — which action ran, whether it succeeded, when, and
-- the captured Helm output. logs is persisted at terminal because the runner's
-- TaskRun pod is garbage-collected after the run, so this is the durable copy.
--
-- Both FKs are ON DELETE CASCADE: a run disappears with its application (which
-- in turn cascades from its organization). organization_id is carried directly
-- so every query stays org-scoped (the tenancy boundary), not via the app join.

CREATE TABLE deployments (
    id                UUID PRIMARY KEY,
    organization_id   UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    application_id    UUID NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    action            TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'running',
    message           TEXT NOT NULL DEFAULT '',
    job_id            TEXT NOT NULL DEFAULT '',
    run_name          TEXT NOT NULL DEFAULT '',
    logs              TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL,
    finished_at       TIMESTAMPTZ,
    updated_at        TIMESTAMPTZ NOT NULL
);

CREATE INDEX deployments_organization_id ON deployments (organization_id);
CREATE INDEX deployments_application_id_created_at ON deployments (application_id, created_at);
CREATE INDEX deployments_organization_id_job_id ON deployments (organization_id, job_id);
