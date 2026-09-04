-- Run triggers. applications.push_trigger is what a GitHub push to a branch a
-- component tracks starts: '' (nothing), 'preview', or 'deploy';
-- applications.pr_plans turns on speculative preview runs for pull requests
-- against a tracked branch, reported back to GitHub as a check run.
-- workflow_runs.trigger records how a triggered run was started (JSON: the
-- repository, branch, commit, sender, and the check run it reports to);
-- empty for a run a person or the scheduler started.
ALTER TABLE applications ADD COLUMN push_trigger TEXT NOT NULL DEFAULT '';
ALTER TABLE applications ADD COLUMN pr_plans BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE workflow_runs ADD COLUMN trigger TEXT NOT NULL DEFAULT '';
