-- Values-from-git sources: an ordered list of git repos to pull values files
-- from, layered (in order) beneath an application's inline values override. Each
-- element is a flat object (repo_url, git_ref, path), the same stringly-typed
-- shape as config, so the schema needs no domain type. Empty list for an app
-- whose values are inline-only.

ALTER TABLE applications
    ADD COLUMN values_sources JSONB NOT NULL DEFAULT '[]'::jsonb;
