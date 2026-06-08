-- Workflow variables: named key/value pairs injected into an application's
-- component jobs as environment variables. component_id distinguishes the two
-- levels: NULL is an app-level variable (passed to every component job); a set
-- component_id is a component-level variable that overrides an app-level variable
-- of the same name for that one component.
--
-- A variable is either non-secret (value stored/returned in plaintext) or
-- sensitive (value envelope-encrypted into encrypted_value and never returned;
-- see lib/secrets), mirroring chart_credentials.encrypted_password.
--
-- organization_id / application_id are carried directly so every query stays
-- org-scoped (the tenancy boundary), and both FKs are ON DELETE CASCADE (a
-- variable disappears with its application, which in turn cascades from its
-- organization). component_id is a bare nullable column with NO FK on purpose:
-- ReplaceWorkflow deletes and recreates every component (stable ids) on each
-- workflow save, so a cascading FK would wipe component variables every save;
-- orphans are reconciled inside that transaction instead.
CREATE TABLE variables (
    id              UUID PRIMARY KEY,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    application_id  UUID NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    component_id    UUID,
    name            TEXT NOT NULL,
    sensitive       BOOLEAN NOT NULL DEFAULT FALSE,
    value           TEXT NOT NULL DEFAULT '',
    encrypted_value BYTEA,
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX variables_organization_id ON variables (organization_id);
CREATE INDEX variables_application_id_component_id ON variables (application_id, component_id);

-- Variable names are unique within their scope. Two partial unique indexes
-- because component_id is nullable: app-level names are unique per application,
-- component-level names are unique per component.
CREATE UNIQUE INDEX variables_app_scope_name ON variables (application_id, name) WHERE component_id IS NULL;
CREATE UNIQUE INDEX variables_component_scope_name ON variables (component_id, name) WHERE component_id IS NOT NULL;
