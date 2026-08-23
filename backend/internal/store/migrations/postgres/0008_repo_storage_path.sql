-- Decouple a repository's physical location from its logical name so that
-- transferring or renaming a repository never moves data
-- (docs/repo-transfer-design.md §3).
--
-- storage_path is assigned once, at creation, and never changes. The WAL
-- prefix (wal/{storage_path}/) and the local bare directory
-- ({root}/{storage_path}.git) are derived from it; exports/ keep using the
-- logical name because their value is the human-readable path.
--
-- Existing rows are backfilled with their *current* physical location
-- ("{models|datasets}/{ns}/{name}"), so nothing on disk or in the bucket has
-- to move. New repositories get an opaque "repos/{ulid}".
ALTER TABLE repositories ADD COLUMN IF NOT EXISTS storage_path TEXT NOT NULL DEFAULT '';

UPDATE repositories r
   SET storage_path = (CASE r.kind WHEN 'model' THEN 'models/' ELSE 'datasets/' END)
                      || (SELECT n.name FROM namespaces n WHERE n.id = r.namespace_id)
                      || '/' || r.name
 WHERE r.storage_path = '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_repositories_storage_path ON repositories (storage_path);
