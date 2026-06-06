-- Phase 7 drop: the DESTRUCTIVE half of the workflow-model migration. The
-- previous migration (..._backfill_workflow_model.sql) already moved every
-- legacy application column down onto a helm Component and turned each
-- deployment into a WorkflowRun + ComponentRun, so the moved columns and the
-- deployments table are now redundant. Slim Application to the workflow-owner
-- shape: { id, organization_id, name, imported, target_namespace,
-- target_cluster_id, runner_cluster_id, created_at, updated_at }.
--
-- Indexes on dropped columns (chart_credential_id, github_installation_id) and
-- their FK constraints are dropped automatically with the columns.

DROP TABLE IF EXISTS deployments;

ALTER TABLE applications
    DROP COLUMN IF EXISTS chart_source,
    DROP COLUMN IF EXISTS config,
    DROP COLUMN IF EXISTS values,
    DROP COLUMN IF EXISTS values_sources,
    DROP COLUMN IF EXISTS release_name,
    DROP COLUMN IF EXISTS chart_credential_id,
    DROP COLUMN IF EXISTS github_installation_id,
    DROP COLUMN IF EXISTS type,
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS status_message,
    DROP COLUMN IF EXISTS job_id,
    DROP COLUMN IF EXISTS last_run_name,
    DROP COLUMN IF EXISTS sync_status,
    DROP COLUMN IF EXISTS sync_message,
    DROP COLUMN IF EXISTS last_diff,
    DROP COLUMN IF EXISTS desired_chart_revision,
    DROP COLUMN IF EXISTS desired_values_revision,
    DROP COLUMN IF EXISTS last_refreshed_at,
    DROP COLUMN IF EXISTS sync_job_id,
    DROP COLUMN IF EXISTS sync_run_name;
