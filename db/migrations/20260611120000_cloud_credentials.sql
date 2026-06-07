-- Cloud credentials: named cloud-provider credential sets (AWS, GCP, Azure)
-- registered to an organization. The foundation for features that authenticate
-- to a cloud (cluster registration, private packages in workflows, …).
-- provider is fixed at creation; config holds non-secret identifiers (region,
-- project, tenant/subscription id, …); encrypted_credentials holds the
-- envelope-encrypted secret material and is never returned to the browser.
-- Names are unique within an organization.

CREATE TABLE cloud_credentials (
    id                    UUID PRIMARY KEY,
    organization_id       UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name                  TEXT NOT NULL,
    provider              TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    config                JSONB NOT NULL DEFAULT '{}'::jsonb,
    encrypted_credentials BYTEA,
    created_at            TIMESTAMPTZ NOT NULL,
    updated_at            TIMESTAMPTZ NOT NULL
);

CREATE INDEX cloud_credentials_organization_id ON cloud_credentials (organization_id);
CREATE UNIQUE INDEX cloud_credentials_organization_id_name ON cloud_credentials (organization_id, name);
