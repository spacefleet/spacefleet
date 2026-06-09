-- Top-level folders for organizing an organization's applications. A group holds
-- applications (via applications.group_id), an application belongs to at most one
-- group, and groups do not nest. It is purely an organizational convenience —
-- nothing about deployment, targeting, or the workflow depends on it.
--
-- Like every resource the table carries organization_id directly so every query
-- stays org-scoped (the tenancy boundary). organization_id is ON DELETE CASCADE
-- (a group disappears with its org). Group names are unique within an org.

CREATE TABLE application_groups (
    id                UUID PRIMARY KEY,
    organization_id   UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL
);

CREATE INDEX application_groups_organization_id ON application_groups (organization_id);
CREATE UNIQUE INDEX application_groups_organization_id_name ON application_groups (organization_id, name);

-- An application's optional membership in a group (folder). ON DELETE SET NULL:
-- deleting a group ungroups its applications (they fall back to the org root)
-- rather than deleting them.
ALTER TABLE applications
    ADD COLUMN group_id UUID REFERENCES application_groups(id) ON DELETE SET NULL;

CREATE INDEX applications_group_id ON applications (group_id);
