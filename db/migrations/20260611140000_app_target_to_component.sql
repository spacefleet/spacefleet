-- Move deploy targeting from the application to the component. The application
-- no longer carries an app-level default target cluster or namespace; targeting
-- now lives exclusively on the components (target_cluster_id / target_namespace,
-- which already exist on the components table). The runner_cluster_id stays on
-- the application — it is the Tekton execution cluster, not a deploy target.
--
-- Dropping target_cluster_id also drops its FK to clusters(id). No backfill:
-- existing components keep whatever target they already had, and any that relied
-- on the app-level default must be re-targeted via the workflow builder.

ALTER TABLE applications
    DROP COLUMN target_cluster_id,
    DROP COLUMN target_namespace;
