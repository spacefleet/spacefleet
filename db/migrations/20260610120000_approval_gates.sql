-- Manual-approval gates for workflow components. requires_approval is the general
-- per-component flag: when set, a run pauses ("awaiting_approval") at that node and
-- waits for a human to approve before it executes. approved_by / approved_at on a
-- component_run record who approved a parked step and when (empty/null otherwise).
--
-- The status columns on component_runs and workflow_runs are plain TEXT with no
-- CHECK constraint, so the new "awaiting_approval" enum value needs no DDL here.

ALTER TABLE components
    ADD COLUMN requires_approval BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE component_runs
    ADD COLUMN approved_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN approved_at TIMESTAMPTZ;
