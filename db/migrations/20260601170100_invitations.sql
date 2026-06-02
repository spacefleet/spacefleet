-- Invitations: a pending request for someone to join an organization with a
-- given role. The token is the bearer secret embedded in the invite link;
-- whoever holds a valid, unexpired link and is signed in joins with the
-- invitation's role. Invitations are marked accepted/revoked rather than
-- deleted so admins can see the history. FK cascades with the organization.

CREATE TABLE invitations (
    id              UUID PRIMARY KEY,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    email           TEXT NOT NULL,
    role            TEXT NOT NULL DEFAULT 'viewer',
    token           TEXT NOT NULL UNIQUE,
    status          TEXT NOT NULL DEFAULT 'pending',
    expires_at      TIMESTAMPTZ NOT NULL,
    accepted_at     TIMESTAMPTZ,
    invited_by      UUID,
    created_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX invitations_organization_id ON invitations (organization_id);
