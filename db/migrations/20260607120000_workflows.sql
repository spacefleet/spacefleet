-- Application deploy workflows: an application owns a DAG of typed Components, and
-- each run of that workflow is a WorkflowRun with one ComponentRun per node. These
-- three tables are added alongside the existing application/deployment columns
-- (additive first); a later migration moves config down and drops the old columns.
--
-- All three carry organization_id directly so every query stays org-scoped (the
-- tenancy boundary), not via the application join. application_id / workflow_run_id
-- FKs are ON DELETE CASCADE (a node/run disappears with its parent, which in turn
-- cascades from its organization); cluster/credential/installation FKs are
-- ON DELETE RESTRICT (a resource in use can't be deleted out from under a
-- component). component_runs.component_id is a bare nullable column with NO FK: the
-- source component may be edited or deleted after a run, and a past run's record
-- must survive that.

-- Components: a workflow node. config holds type-specific, non-secret params
-- (validated per type in the service). depends_on lists sibling component ids this
-- node waits on (the DAG edges). position is the canvas {x, y} for the builder UI.
CREATE TABLE components (
    id                     UUID PRIMARY KEY,
    organization_id        UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    application_id         UUID NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    name                   TEXT NOT NULL,
    type                   TEXT NOT NULL DEFAULT 'helm',
    config                 JSONB NOT NULL DEFAULT '{}'::jsonb,
    depends_on             JSONB NOT NULL DEFAULT '[]'::jsonb,
    continue_on_failure    BOOLEAN NOT NULL DEFAULT FALSE,
    target_cluster_id      UUID REFERENCES clusters(id) ON DELETE RESTRICT,
    target_namespace       TEXT NOT NULL DEFAULT '',
    chart_credential_id    UUID REFERENCES chart_credentials(id) ON DELETE RESTRICT,
    github_installation_id UUID REFERENCES github_installations(id) ON DELETE RESTRICT,
    position               JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at             TIMESTAMPTZ NOT NULL,
    updated_at             TIMESTAMPTZ NOT NULL
);

CREATE INDEX components_organization_id ON components (organization_id);
CREATE INDEX components_application_id ON components (application_id);
CREATE INDEX components_target_cluster_id ON components (target_cluster_id);
CREATE INDEX components_chart_credential_id ON components (chart_credential_id);
CREATE INDEX components_github_installation_id ON components (github_installation_id);

-- Workflow runs: one logical run of an application's workflow. graph is a JSON
-- snapshot of the nodes + edges + config as the run began, so an in-flight run is
-- immune to later edits. status settles succeeded/failed/partial.
CREATE TABLE workflow_runs (
    id                UUID PRIMARY KEY,
    organization_id   UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    application_id    UUID NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    action            TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'pending',
    message           TEXT NOT NULL DEFAULT '',
    job_id            TEXT NOT NULL DEFAULT '',
    graph             TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL,
    started_at        TIMESTAMPTZ,
    finished_at       TIMESTAMPTZ,
    updated_at        TIMESTAMPTZ NOT NULL
);

CREATE INDEX workflow_runs_organization_id ON workflow_runs (organization_id);
CREATE INDEX workflow_runs_application_id_created_at ON workflow_runs (application_id, created_at);
CREATE INDEX workflow_runs_organization_id_job_id ON workflow_runs (organization_id, job_id);

-- Component runs: the execution of one node within a workflow run. component_id is
-- a bare nullable column with NO FK so a past run survives the component being
-- edited or deleted; name/type are denormalized snapshots for the same reason.
-- logs is persisted at terminal (the runner pod is garbage-collected after).
CREATE TABLE component_runs (
    id                UUID PRIMARY KEY,
    organization_id   UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    workflow_run_id   UUID NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    component_id      UUID,
    name              TEXT NOT NULL DEFAULT '',
    type              TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'pending',
    message           TEXT NOT NULL DEFAULT '',
    run_name          TEXT NOT NULL DEFAULT '',
    logs              TEXT NOT NULL DEFAULT '',
    chart_revision    TEXT NOT NULL DEFAULT '',
    values_revision   TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL,
    started_at        TIMESTAMPTZ,
    finished_at       TIMESTAMPTZ,
    updated_at        TIMESTAMPTZ NOT NULL
);

CREATE INDEX component_runs_organization_id ON component_runs (organization_id);
CREATE INDEX component_runs_workflow_run_id ON component_runs (workflow_run_id);
