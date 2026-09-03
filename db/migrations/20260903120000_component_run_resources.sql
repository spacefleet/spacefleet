-- Managed-resource inventory captured from a terraform apply step that
-- succeeded on a deploy run: a JSON array of
-- {"address","mode","type","name","provider","id"} records — the state
-- reduced to one line per resource in the pod, never the full state (which
-- carries every attribute value). Empty for every other step.
ALTER TABLE component_runs ADD COLUMN resources TEXT NOT NULL DEFAULT '';

-- Serves the "latest state" lookup: the most recent succeeded apply with a
-- captured inventory for a component, org-scoped.
CREATE INDEX component_runs_latest_resources ON component_runs (organization_id, component_id, finished_at DESC)
    WHERE status = 'succeeded' AND resources <> '';
