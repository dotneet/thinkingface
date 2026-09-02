-- See the postgres migration (0034) for the reasoning.
--
-- SQLite only accepts a constant DEFAULT in ALTER TABLE ADD COLUMN, and the
-- default wanted here is "now", so the column is added without one and the
-- existing rows are stamped by the UPDATE below. Every INSERT into this table
-- writes created_at explicitly (store/files.go, dialect_sqlite.go), so no
-- later row is left NULL -- and the prune's predicate is written so that a
-- NULL would be kept rather than collected, which is the safe direction.
ALTER TABLE repo_lfs_objects ADD COLUMN created_at DATETIME;

UPDATE repo_lfs_objects SET created_at = now() WHERE created_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_repo_lfs_objects_repo_created
    ON repo_lfs_objects (repo_id, created_at);
