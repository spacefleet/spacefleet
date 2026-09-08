-- Policy checks. policies holds an organization's Rego policies evaluated
-- against every OpenTofu plan of the applications they cover, before the
-- apply: enforcement 'block' fails the plan step on a violation (the apply
-- never runs), 'warn' records it for the approver. application_id limits a
-- policy to one application (NULL = every application). Names are unique
-- within an organization. component_runs.policy records the verdict
-- (policy.Verdict JSON) on the plan step it was evaluated for.

CREATE TABLE policies (
    id              UUID PRIMARY KEY,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    rego            TEXT NOT NULL,
    enforcement     TEXT NOT NULL DEFAULT 'block',
    enabled         BOOLEAN NOT NULL DEFAULT true,
    application_id  UUID REFERENCES applications(id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX policies_organization_id ON policies (organization_id);
CREATE UNIQUE INDEX policies_organization_id_name ON policies (organization_id, name);

ALTER TABLE component_runs ADD COLUMN policy TEXT NOT NULL DEFAULT '';
