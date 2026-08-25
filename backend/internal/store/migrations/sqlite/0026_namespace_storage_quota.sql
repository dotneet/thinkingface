-- See the postgres migration (0032) for the reasoning.
--
-- SQLite has no ADD COLUMN IF NOT EXISTS, but every migration runs exactly
-- once (schema_migrations records it by file name), so a plain ADD COLUMN is
-- safe. INTEGER is the declared type, which is SQLite's 64-bit integer and so
-- the counterpart of Postgres' BIGINT.
ALTER TABLE namespaces ADD COLUMN storage_quota_bytes INTEGER
    CHECK (storage_quota_bytes IS NULL OR storage_quota_bytes >= 0);
