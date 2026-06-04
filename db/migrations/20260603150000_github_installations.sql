-- GitHub installations: an organization's installation of the operator's GitHub
-- App, used to pull charts from private Git repositories. Stores only the
-- numeric GitHub installation_id plus the account it is installed on
-- (account_login/account_type) for display — no secret. At rollout time the
-- backend mints a short-lived installation access token from the operator's App
-- private key, so nothing credential-bearing lives here. Surfaced in the UI
-- under "GitHub".
--
-- The org FK is ON DELETE CASCADE (an installation disappears with its
-- organization). The applications.github_installation_id FK added below is ON
-- DELETE RESTRICT: an installation in use by an app can't be deleted out from
-- under it (surfaced as a 409 conflict).

CREATE TABLE github_installations (
    id              UUID PRIMARY KEY,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    installation_id BIGINT NOT NULL,
    account_login   TEXT NOT NULL DEFAULT '',
    account_type    TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX github_installations_organization_id ON github_installations (organization_id);
CREATE UNIQUE INDEX github_installations_organization_id_installation_id ON github_installations (organization_id, installation_id);

-- Attach point on applications: optional, RESTRICT so an installation in use
-- can't be deleted.
ALTER TABLE applications
    ADD COLUMN github_installation_id UUID REFERENCES github_installations(id) ON DELETE RESTRICT;

CREATE INDEX applications_github_installation_id ON applications (github_installation_id);
