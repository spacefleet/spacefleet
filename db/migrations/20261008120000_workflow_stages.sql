-- Workflow stages: an application's workflow becomes an ordered list of named
-- stages, each holding components that run in parallel. Stages run in order —
-- a stage starts once every component of the previous stage has finished. This
-- replaces the free-form graph: per-component depends_on edges, the
-- component_groups containers, and the canvas positions.
--
-- This is the ADDITIVE half: it creates workflow_stages, adds
-- components.stage_id/ordinal, and backfills every existing workflow into
-- stages. The next migration (..._drop_workflow_graph_columns.sql) drops the
-- replaced columns and table once this data is in place. Both halves are split
-- so the destructive step is isolated.
--
-- Like every resource the table carries organization_id directly so every query
-- stays org-scoped (the tenancy boundary). application_id is ON DELETE CASCADE
-- (a stage disappears with its application), and so is components.stage_id (a
-- component disappears with its stage). ordinal is the stage's position in run
-- order, 0-based.

CREATE TABLE workflow_stages (
    id                UUID PRIMARY KEY,
    organization_id   UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    application_id    UUID NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    ordinal           INTEGER NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL
);

CREATE INDEX workflow_stages_organization_id ON workflow_stages (organization_id);
CREATE INDEX workflow_stages_application_id_ordinal ON workflow_stages (application_id, ordinal);

-- stage_id starts nullable so the backfill below can fill it; it is set NOT
-- NULL at the end. ordinal is the component's display order within its stage.
ALTER TABLE components
    ADD COLUMN stage_id UUID REFERENCES workflow_stages(id) ON DELETE CASCADE,
    ADD COLUMN ordinal  INTEGER NOT NULL DEFAULT 0;

CREATE INDEX components_stage_id ON components (stage_id);

-- Backfill. Each component's stage is its depth in the old graph: the length
-- of its longest chain of dependencies (no dependencies = the first stage).
-- That keeps every existing ordering, since a dependency always lands in an
-- earlier stage; the cost is that a component now waits for its whole previous
-- stage rather than just the components it named. Group references expand the
-- way the scheduler saw them: depending on a group means depending on every
-- member, and a group's own dependencies apply to each of its members.
--
-- depends_on was written as JSON null for an empty list by some code paths, so
-- anything that isn't an array reads as no dependencies, and an entry that
-- isn't a uuid is skipped rather than failing the migration.
CREATE TEMPORARY TABLE stage_backfill ON COMMIT DROP AS
WITH RECURSIVE
raw_refs AS (
    -- Every authored reference: the component's own depends_on, plus its
    -- group's depends_on when it is a member of one.
    SELECT c.id AS component_id, r.ref
    FROM components c
    CROSS JOIN LATERAL jsonb_array_elements_text(
        CASE WHEN jsonb_typeof(c.depends_on) = 'array' THEN c.depends_on ELSE '[]'::jsonb END
    ) AS r(ref)
    UNION
    SELECT c.id, r.ref
    FROM components c
    JOIN component_groups g ON g.id = c.group_id
    CROSS JOIN LATERAL jsonb_array_elements_text(
        CASE WHEN jsonb_typeof(g.depends_on) = 'array' THEN g.depends_on ELSE '[]'::jsonb END
    ) AS r(ref)
),
refs AS (
    SELECT component_id, ref::uuid AS ref
    FROM raw_refs
    WHERE ref ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
),
edges AS (
    -- A reference to a component of the same application is an edge as-is.
    SELECT r.component_id, d.id AS dep
    FROM refs r
    JOIN components c ON c.id = r.component_id
    JOIN components d ON d.id = r.ref AND d.application_id = c.application_id
    WHERE d.id <> r.component_id
    UNION
    -- A reference to a group is an edge to each of its members.
    SELECT r.component_id, m.id
    FROM refs r
    JOIN components m ON m.group_id = r.ref
    WHERE m.id <> r.component_id
),
depth AS (
    SELECT c.id AS component_id, 0 AS depth FROM components c
    UNION
    SELECT e.component_id, d.depth + 1
    FROM edges e
    JOIN depth d ON d.component_id = e.dep
    -- The graph was validated acyclic on every save; the bound only keeps a
    -- corrupt row from looping forever.
    WHERE d.depth < 1000
)
SELECT component_id, max(depth) AS depth
FROM depth
GROUP BY component_id;

-- One stage per (application, depth). Longest-path depths are contiguous from
-- zero, so depth is the stage's ordinal directly. A stage whose components are
-- exactly one group's members keeps that group's name; any other stage is
-- "Stage N".
WITH layer AS (
    SELECT
        c.organization_id,
        c.application_id,
        b.depth,
        count(*)                                                        AS n,
        count(c.group_id)                                               AS n_grouped,
        count(DISTINCT c.group_id)                                      AS n_groups,
        (array_agg(c.group_id) FILTER (WHERE c.group_id IS NOT NULL))[1] AS group_id
    FROM stage_backfill b
    JOIN components c ON c.id = b.component_id
    GROUP BY c.organization_id, c.application_id, b.depth
),
named AS (
    SELECT
        l.organization_id,
        l.application_id,
        l.depth,
        CASE
            WHEN l.n = l.n_grouped
             AND l.n_groups = 1
             AND NOT EXISTS (
                 SELECT 1
                 FROM components m
                 JOIN stage_backfill mb ON mb.component_id = m.id
                 WHERE m.group_id = l.group_id AND mb.depth <> l.depth
             )
            THEN left(btrim(g.name), 100)
        END AS group_name
    FROM layer l
    LEFT JOIN component_groups g ON g.id = l.group_id
)
INSERT INTO workflow_stages (id, organization_id, application_id, name, ordinal, created_at, updated_at)
SELECT
    gen_random_uuid(),
    organization_id,
    application_id,
    COALESCE(NULLIF(group_name, ''), 'Stage ' || (depth + 1)),
    depth,
    now(),
    now()
FROM named;

-- Place each component in its stage, ordered left to right as it sat on the
-- canvas (a group member's position was relative to its group), then by age.
-- A position without a numeric x sorts last.
UPDATE components c
SET stage_id = s.id,
    ordinal  = o.ordinal
FROM (
    SELECT
        c2.id,
        b.depth,
        ROW_NUMBER() OVER (
            PARTITION BY c2.application_id, b.depth
            ORDER BY
                CASE WHEN jsonb_typeof(c2.position -> 'x') = 'number'
                     THEN (c2.position ->> 'x')::double precision END
                    + CASE WHEN jsonb_typeof(g.position -> 'x') = 'number'
                           THEN (g.position ->> 'x')::double precision ELSE 0 END NULLS LAST,
                c2.created_at,
                c2.id
        ) - 1 AS ordinal
    FROM components c2
    JOIN stage_backfill b ON b.component_id = c2.id
    LEFT JOIN component_groups g ON g.id = c2.group_id
) o
JOIN workflow_stages s ON s.ordinal = o.depth
WHERE o.id = c.id
  AND s.application_id = c.application_id;

ALTER TABLE components ALTER COLUMN stage_id SET NOT NULL;
