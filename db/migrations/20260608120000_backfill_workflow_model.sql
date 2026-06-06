-- Phase 7 backfill: migrate the legacy single-helm Application model onto the
-- workflow model (Application owns Components; a run is a WorkflowRun with one
-- ComponentRun per node). This is the ADDITIVE half — it only INSERTs the
-- backfilled rows; the next migration (..._drop_legacy_application_columns.sql)
-- drops the moved columns and the deployments table once this data is in place.
--
-- ORDER MATTERS: this runs before the drop so no source column is read after it
-- is gone. Both halves are split so the destructive step is isolated.

-- 1. One helm Component per existing application that has none yet. Apps already
--    migrated to the workflow model via the UI (they have a component) are left
--    untouched by the NOT EXISTS guard, so this never double-creates a node.
--
--    The component's config jsonb is built from the app's legacy columns: the
--    app's `config` jsonb (repo_url/chart/version/git_ref/git_path) is the base,
--    merged with chart_source, the inline values (-> 'values'), the git
--    values_sources (-> 'values_sources', stored as a JSON *string* the way the
--    service round-trips it), and release_name (only when non-empty). Credentials
--    and the github installation carry over as FK columns. target_* are left at
--    their defaults (NULL / '') so the component inherits the app's defaults.
INSERT INTO components (
    id,
    organization_id,
    application_id,
    name,
    type,
    config,
    depends_on,
    continue_on_failure,
    target_cluster_id,
    target_namespace,
    chart_credential_id,
    github_installation_id,
    position,
    created_at,
    updated_at
)
SELECT
    gen_random_uuid(),
    a.organization_id,
    a.id,
    a.name,
    'helm',
    -- Base coordinates from the app's config jsonb, then layer the rest on top.
    a.config
        || jsonb_build_object('chart_source', a.chart_source)
        || jsonb_build_object('values', a.values)
        || jsonb_build_object('values_sources', a.values_sources::text)
        || CASE WHEN a.release_name <> '' THEN jsonb_build_object('release_name', a.release_name) ELSE '{}'::jsonb END,
    '[]'::jsonb,
    FALSE,
    NULL,
    '',
    a.chart_credential_id,
    a.github_installation_id,
    '{}'::jsonb,
    now(),
    now()
FROM applications a
WHERE NOT EXISTS (
    SELECT 1 FROM components c WHERE c.application_id = a.id
);

-- 2. One WorkflowRun + one ComponentRun per existing deployment (rollout-history
--    row). There may be zero deployments, but this is written to be correct if
--    not. The deployment's action maps straight across (deploy/upgrade/uninstall
--    are all valid run actions except "upgrade" — fold upgrade into deploy, the
--    workflow model has no separate upgrade action). status maps
--    running->running, succeeded->succeeded, failed->failed. graph is a minimal
--    single-node JSON snapshot (the app's backfilled component). The matching
--    ComponentRun carries the deployment's logs/revisions and run_name, with
--    name/type from the app's backfilled component.
WITH src AS (
    SELECT
        d.id            AS deployment_id,
        d.organization_id,
        d.application_id,
        CASE WHEN d.action = 'uninstall' THEN 'uninstall' ELSE 'deploy' END AS run_action,
        d.status,
        d.message,
        d.run_name,
        d.logs,
        d.chart_revision,
        d.values_revision,
        d.created_at,
        d.finished_at,
        c.id            AS component_id,
        c.name          AS component_name,
        c.type          AS component_type,
        gen_random_uuid() AS run_id,
        gen_random_uuid() AS component_run_id
    FROM deployments d
    JOIN LATERAL (
        SELECT cc.id, cc.name, cc.type
        FROM components cc
        WHERE cc.application_id = d.application_id
        ORDER BY cc.created_at ASC
        LIMIT 1
    ) c ON TRUE
),
ins_run AS (
    INSERT INTO workflow_runs (
        id, organization_id, application_id, action, status, message, job_id,
        graph, created_at, started_at, finished_at, updated_at
    )
    SELECT
        s.run_id,
        s.organization_id,
        s.application_id,
        s.run_action,
        s.status,
        s.message,
        '',
        jsonb_build_object('nodes', jsonb_build_array(jsonb_build_object(
            'id', s.component_id,
            'name', s.component_name,
            'type', s.component_type,
            'depends_on', '[]'::jsonb
        )))::text,
        s.created_at,
        s.created_at,
        s.finished_at,
        now()
    FROM src s
    RETURNING id
)
INSERT INTO component_runs (
    id, organization_id, workflow_run_id, component_id, name, type, status,
    message, run_name, logs, chart_revision, values_revision, created_at,
    started_at, finished_at, updated_at
)
SELECT
    s.component_run_id,
    s.organization_id,
    s.run_id,
    s.component_id,
    s.component_name,
    s.component_type,
    s.status,
    s.message,
    s.run_name,
    s.logs,
    s.chart_revision,
    s.values_revision,
    s.created_at,
    s.created_at,
    s.finished_at,
    now()
FROM src s;
