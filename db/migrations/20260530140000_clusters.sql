-- Clusters: Kubernetes clusters registered to an organization. The first
-- org-scoped resource. connection_method + config say how to reach the cluster;
-- encrypted_credentials holds the envelope-encrypted credential (nil for the
-- in-cluster method); status/k8s_version/last_checked_at record the most recent
-- connectivity probe.

CREATE TABLE clusters (
    id                    UUID PRIMARY KEY,
    organization_id       UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name                  TEXT NOT NULL,
    connection_method     TEXT NOT NULL,
    endpoint              TEXT NOT NULL DEFAULT '',
    config                JSONB NOT NULL DEFAULT '{}'::jsonb,
    encrypted_credentials BYTEA,
    status                TEXT NOT NULL DEFAULT 'pending',
    status_message        TEXT NOT NULL DEFAULT '',
    k8s_version           TEXT NOT NULL DEFAULT '',
    last_checked_at       TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL,
    updated_at            TIMESTAMPTZ NOT NULL
);

CREATE INDEX clusters_organization_id ON clusters (organization_id);
CREATE UNIQUE INDEX clusters_organization_id_name ON clusters (organization_id, name);
