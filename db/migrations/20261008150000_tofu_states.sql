-- Managed OpenTofu state. Spacefleet serves OpenTofu's `http` backend for
-- components whose backend is "spacefleet", and keeps the state here.
--
-- tofu_states is one row per (organization, application, component,
-- workspace): the current version pointer, the serial and lineage of that
-- version (the write guards compare against them), and the lock. The lock
-- is taken with a conditional UPDATE, so the database is the only arbiter
-- across serve replicas. locked_by_component_run_id records which step holds
-- it, so a settled run's locks can be found and released.
--
-- component_id deliberately has no foreign key: a workflow save deletes and
-- recreates its components (keeping their ids), and removing a component
-- from the workflow must not drop state that still tracks real
-- infrastructure. The state goes when the application does.
--
-- tofu_state_versions is append-only: every accepted write is a new version
-- holding the state gzipped and then sealed with SPACEFLEET_SECRET_KEY. The
-- run ids are recorded without foreign keys so the history outlives a run.

CREATE TABLE tofu_states (
    id                          UUID PRIMARY KEY,
    organization_id             UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    application_id              UUID NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    component_id                UUID NOT NULL,
    workspace                   TEXT NOT NULL DEFAULT 'default',
    current_version             INTEGER NOT NULL DEFAULT 0,
    serial                      BIGINT NOT NULL DEFAULT 0,
    lineage                     TEXT NOT NULL DEFAULT '',
    lock_id                     TEXT,
    lock_info                   JSONB,
    locked_at                   TIMESTAMPTZ,
    locked_by_component_run_id  UUID,
    created_at                  TIMESTAMPTZ NOT NULL,
    updated_at                  TIMESTAMPTZ NOT NULL
);

CREATE INDEX tofu_states_organization_id ON tofu_states (organization_id);
CREATE UNIQUE INDEX tofu_states_identity ON tofu_states (organization_id, application_id, component_id, workspace);
CREATE INDEX tofu_states_locked_by ON tofu_states (locked_by_component_run_id) WHERE locked_by_component_run_id IS NOT NULL;

CREATE TABLE tofu_state_versions (
    id                UUID PRIMARY KEY,
    state_id          UUID NOT NULL REFERENCES tofu_states(id) ON DELETE CASCADE,
    organization_id   UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    version           INTEGER NOT NULL,
    serial            BIGINT NOT NULL,
    lineage           TEXT NOT NULL,
    sealed            BYTEA NOT NULL,
    size_bytes        BIGINT NOT NULL,
    md5               TEXT NOT NULL,
    workflow_run_id   UUID,
    component_run_id  UUID,
    created_by        TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL
);

CREATE INDEX tofu_state_versions_organization_id ON tofu_state_versions (organization_id);
CREATE UNIQUE INDEX tofu_state_versions_state_id_version ON tofu_state_versions (state_id, version);
CREATE INDEX tofu_state_versions_component_run_id ON tofu_state_versions (component_run_id) WHERE component_run_id IS NOT NULL;
