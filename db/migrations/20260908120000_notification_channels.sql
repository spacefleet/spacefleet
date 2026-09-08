-- Notification channels: where an organization's run notifications go — an
-- email address, a Slack incoming webhook, or a generic webhook — with the
-- events each receives (awaiting_approval, run_failed, drift_detected) and an
-- optional single application to limit it to. A webhook URL is a secret, so
-- it is envelope-encrypted into encrypted_target; address is the display
-- form only (the email address, or the webhook's host). Names are unique
-- within an organization.

CREATE TABLE notification_channels (
    id               UUID PRIMARY KEY,
    organization_id  UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    kind             TEXT NOT NULL,
    address          TEXT NOT NULL DEFAULT '',
    encrypted_target BYTEA,
    events           JSONB NOT NULL DEFAULT '[]'::jsonb,
    application_id   UUID REFERENCES applications(id) ON DELETE CASCADE,
    created_at       TIMESTAMPTZ NOT NULL,
    updated_at       TIMESTAMPTZ NOT NULL
);

CREATE INDEX notification_channels_organization_id ON notification_channels (organization_id);
CREATE UNIQUE INDEX notification_channels_organization_id_name ON notification_channels (organization_id, name);
