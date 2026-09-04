-- Per-run arguments for actions that take them: the JSON of a state_op run's
-- operation and typed fields (force-unlock lock id, state rm/mv addresses,
-- import address + id). Empty for every other action. Text like graph — the
-- service marshals/unmarshals.
ALTER TABLE workflow_runs ADD COLUMN args TEXT NOT NULL DEFAULT '';
