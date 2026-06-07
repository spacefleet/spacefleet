-- Explicit group containers for a workflow: a named box holding components that
-- run in parallel. A node that depends on a group waits for all of its members
-- (all-must-complete); a group's depends_on makes every member wait on those
-- refs. Groups are a builder/persistence concept that desugar into
-- component-level depends_on at validate/snapshot time — the scheduler never
-- sees a group.
--
-- Like every resource the table carries organization_id directly so every query
-- stays org-scoped (the tenancy boundary), not via the application join.
-- application_id is ON DELETE CASCADE (a group disappears with its application,
-- which in turn cascades from its organization). depends_on stores ids of
-- components/groups the whole group waits on (entries may reference either).
-- position/size are the canvas {x,y} / {w,h} for the builder UI.

CREATE TABLE component_groups (
    id                UUID PRIMARY KEY,
    organization_id   UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    application_id    UUID NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    depends_on        JSONB NOT NULL DEFAULT '[]'::jsonb,
    position          JSONB NOT NULL DEFAULT '{}'::jsonb,
    size              JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL
);

CREATE INDEX component_groups_organization_id ON component_groups (organization_id);
CREATE INDEX component_groups_application_id ON component_groups (application_id);

-- A component's optional membership in a group. ON DELETE SET NULL: deleting a
-- group ungroups its members rather than deleting them.
ALTER TABLE components
    ADD COLUMN group_id UUID REFERENCES component_groups(id) ON DELETE SET NULL;

CREATE INDEX components_group_id ON components (group_id);
