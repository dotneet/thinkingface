-- See the postgres migration (0028) for the reasoning. SQLite runs a single
-- writer, so the serialisation itself is never contended here, but the sibling
-- lookup wants the same index.
CREATE INDEX IF NOT EXISTS idx_sync_jobs_repo_ref_status ON sync_jobs (repo_id, ref, status);
