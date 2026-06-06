-- Mark applications adopted from a release already running on the cluster (the
-- import flow) rather than created and deployed by Spacefleet. An imported app
-- starts in the deployed state without a rollout; the flag lets the UI flag it
-- and prompt a refresh to confirm the configured chart source reproduces the
-- live release. Defaults false so every existing row is treated as created.

ALTER TABLE applications
    ADD COLUMN imported BOOLEAN NOT NULL DEFAULT false;
