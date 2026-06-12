-- Structured outputs captured from a terraform apply step that succeeded on a
-- deploy run: tofu's `output -json` shape, a JSON object
-- {"<name>": {"value": <json>, "type": <json>, "sensitive": bool}}. Empty for
-- every other step.
ALTER TABLE component_runs ADD COLUMN outputs TEXT NOT NULL DEFAULT '';

-- Serves the "latest successful outputs" lookup: the most recent succeeded run
-- with captured outputs for a component, org-scoped — the fallback previews
-- and partial runs resolve upstream output references against.
CREATE INDEX component_runs_latest_outputs ON component_runs (organization_id, component_id, finished_at DESC)
    WHERE status = 'succeeded' AND outputs <> '';
