-- See the postgres migration (0027) for the reasoning. SQLite needs the index
-- dropped before the column: ALTER TABLE ... DROP COLUMN refuses a column an
-- index still references. DROP COLUMN itself needs SQLite 3.35+, which the
-- pure-Go modernc.org/sqlite driver is well past.
DROP INDEX IF EXISTS idx_lfs_objects_storage_key;
ALTER TABLE lfs_objects DROP COLUMN storage_key;

DROP TABLE IF EXISTS storage_reclaim_jobs;

DELETE FROM sync_jobs WHERE kind = 'relocate_exports';
