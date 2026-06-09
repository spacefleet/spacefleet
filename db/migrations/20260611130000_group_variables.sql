-- Group variables: named key/value pairs defined on an application group and
-- injected into the component jobs of every application in that group as
-- environment variables. They are the lowest-priority variable level — an
-- app-level variable of the same name overrides a group variable, and a
-- component-level one overrides that (group < app < component); the merge lives
-- in lib/variables. See 20260608120000_variables.sql for the app/component table.
--
-- A variable is either non-secret (value stored/returned in plaintext) or
-- sensitive (value envelope-encrypted into encrypted_value and never returned;
-- see lib/secrets), mirroring the variables table.
--
-- organization_id / group_id are carried directly so every query stays
-- org-scoped (the tenancy boundary), and both FKs are ON DELETE CASCADE: a
-- group variable disappears with its group (and, transitively, its org).
-- Deleting a group ungroups its applications rather than deleting them, so
-- their own app/component variables are unaffected.
CREATE TABLE group_variables (
    id              UUID PRIMARY KEY,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    group_id        UUID NOT NULL REFERENCES application_groups(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    sensitive       BOOLEAN NOT NULL DEFAULT FALSE,
    value           TEXT NOT NULL DEFAULT '',
    encrypted_value BYTEA,
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX group_variables_organization_id ON group_variables (organization_id);

-- Variable names are unique within a group; this index also serves listing and
-- resolving a group's variables.
CREATE UNIQUE INDEX group_variables_group_id_name ON group_variables (group_id, name);
