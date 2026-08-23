-- Decouple a repository's physical location from its logical name
-- (postgres/0008_repo_storage_path.sql, docs/dev/repo-transfer-design.md §3).
-- Existing rows keep their current on-disk / WAL location as storage_path;
-- new repositories get "repos/{ulid}".
ALTER TABLE repositories ADD COLUMN storage_path TEXT NOT NULL DEFAULT '';

UPDATE repositories
   SET storage_path = (CASE kind WHEN 'model' THEN 'models/' ELSE 'datasets/' END)
                      || (SELECT n.name FROM namespaces n WHERE n.id = repositories.namespace_id)
                      || '/' || name
 WHERE storage_path = '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_repositories_storage_path ON repositories (storage_path);
