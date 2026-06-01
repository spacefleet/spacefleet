-- Account management: organizations as the top-level tenant, local user
-- records provisioned from OIDC, and the membership join that records which
-- users belong to which organizations (and their role).
--
-- Drops the original example `notes` table if a dev database still has it
-- (a no-op on fresh databases).

DROP TABLE IF EXISTS notes;

CREATE TABLE users (
    id           UUID PRIMARY KEY,
    oidc_subject TEXT NOT NULL UNIQUE,
    email        TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL
);

CREATE TABLE organizations (
    id         UUID PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE memberships (
    id              UUID PRIMARY KEY,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    role            TEXT NOT NULL DEFAULT 'member',
    created_at      TIMESTAMPTZ NOT NULL,
    UNIQUE (user_id, organization_id)
);

CREATE INDEX memberships_user_id ON memberships (user_id);
CREATE INDEX memberships_organization_id ON memberships (organization_id);
