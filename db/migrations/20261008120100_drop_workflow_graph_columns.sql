-- The DESTRUCTIVE half of the move to workflow stages: with every component
-- placed in a stage by the previous migration (..._workflow_stages.sql), drop
-- what the stages replace — the per-component dependency edges, the canvas
-- positions, and the group containers. Past runs keep their own snapshot of the
-- graph they ran, so no run history depends on these.

DROP INDEX IF EXISTS components_group_id;

ALTER TABLE components
    DROP COLUMN group_id,
    DROP COLUMN depends_on,
    DROP COLUMN position;

DROP TABLE component_groups;
