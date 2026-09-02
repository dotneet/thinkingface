-- See the postgres migration (0036) for the reasoning.
--
-- As with created_at in 0028, SQLite only accepts a constant DEFAULT in ALTER
-- TABLE ADD COLUMN, so the column is added bare and the existing rows are
-- stamped by the UPDATE below. Every later row is written explicitly:
-- dialect_sqlite.go's linkLFSObjectsInsert sets committed_at, and
-- store/files.go's RecordLFSObject deliberately leaves it NULL.
ALTER TABLE repo_lfs_objects ADD COLUMN committed_at DATETIME;

UPDATE repo_lfs_objects SET committed_at = now() WHERE committed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_repo_lfs_objects_uncommitted
    ON repo_lfs_objects (repo_id, created_at)
    WHERE committed_at IS NULL;
