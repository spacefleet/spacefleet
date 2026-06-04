-- Chart credentials: named credential sets, registered to an organization, for
-- pulling private Helm charts. type matches the chart source it authenticates
-- (basic_auth → http_repo, oci → oci). username is non-secret display detail;
-- encrypted_password is the envelope-encrypted password blob (see lib/secrets),
-- never returned to the browser. Surfaced in the UI as "Private Charts".
--
-- The org FK is ON DELETE CASCADE (a credential disappears with its
-- organization). The applications.chart_credential_id FK added below is ON
-- DELETE RESTRICT: a credential in use by an app can't be deleted out from under
-- it (surfaced as a 409 conflict).

CREATE TABLE chart_credentials (
    id                 UUID PRIMARY KEY,
    organization_id    UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name               TEXT NOT NULL,
    type               TEXT NOT NULL,
    username           TEXT NOT NULL DEFAULT '',
    encrypted_password BYTEA,
    created_at         TIMESTAMPTZ NOT NULL,
    updated_at         TIMESTAMPTZ NOT NULL
);

CREATE INDEX chart_credentials_organization_id ON chart_credentials (organization_id);
CREATE UNIQUE INDEX chart_credentials_organization_id_name ON chart_credentials (organization_id, name);

-- Attach point on applications: optional, RESTRICT so a credential in use can't
-- be deleted.
ALTER TABLE applications
    ADD COLUMN chart_credential_id UUID REFERENCES chart_credentials(id) ON DELETE RESTRICT;

CREATE INDEX applications_chart_credential_id ON applications (chart_credential_id);
