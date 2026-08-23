-- Repository archive (postgres/0012_repo_archive.sql). SQLite has no
-- ADD COLUMN IF NOT EXISTS, but every migration runs exactly once (recorded
-- in schema_migrations), so the plain form is safe here.
ALTER TABLE repositories ADD COLUMN archived_at DATETIME;
ALTER TABLE repositories ADD COLUMN archived_by INTEGER REFERENCES users (id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_repositories_archived ON repositories (archived_at) WHERE archived_at IS NOT NULL;
