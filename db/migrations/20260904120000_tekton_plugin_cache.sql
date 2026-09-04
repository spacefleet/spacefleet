-- OpenTofu provider plugin cache per runner cluster: a PersistentVolumeClaim in
-- the jobs namespace mounted into every OpenTofu step as TF_PLUGIN_CACHE_DIR.
-- plugin_cache_size is the claim's requested storage (a Kubernetes quantity,
-- e.g. 20Gi); empty means no cache. plugin_cache_storage_class optionally names
-- the StorageClass (empty = the cluster default).
ALTER TABLE tekton_installations
    ADD COLUMN plugin_cache_size TEXT NOT NULL DEFAULT '',
    ADD COLUMN plugin_cache_storage_class TEXT NOT NULL DEFAULT '';
