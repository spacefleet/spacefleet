-- Approval policy. components.approval_policy is the per-component policy
-- applied at its approval gate: {"approvers": [emails], "required": N,
-- "require_different_approver": bool, "timeout_minutes": M}; the empty object
-- is the original any-editor, one-approval, wait-forever behaviour.
-- workflow_runs.started_by records who started a run (email; empty for the
-- scheduler) so no-self-approval can be enforced. component_runs.approvals is
-- the JSON array of every approval recorded on a gate (the N-of-M tally);
-- approved_by/approved_at remain the decision that opened or rejected it.
ALTER TABLE components ADD COLUMN approval_policy JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE workflow_runs ADD COLUMN started_by TEXT NOT NULL DEFAULT '';
ALTER TABLE component_runs ADD COLUMN approvals TEXT NOT NULL DEFAULT '';
