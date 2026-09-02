-- Two writes that had no business being expensive: a file download, and the
-- LFS garbage collector's orphan check.

-- 1. The full-text index was rebuilt on every file download.
--
-- 0001_init.sql attached repositories_search_vector_trigger to
-- `BEFORE INSERT OR UPDATE ON repositories` with no column list and no WHEN
-- guard, so *every* UPDATE on the table recomputed nine to_tsvector() calls
-- over the card. The statement that dominates writes to this table is
-- Store.IncrementDownloads -- `UPDATE repositories SET downloads =
-- downloads + 1`, issued from a detached goroutine on every single resolve --
-- and it cannot change the index by construction. A dataset with heavy
-- download traffic therefore paid for a full re-tokenisation of its model
-- card per downloaded file, and wrote back a search_vector identical to the
-- one already there.
--
-- The index reads exactly three columns: name, description and card (tags,
-- license, pipeline_tag and task_categories are all keys of the card), so
-- `UPDATE OF` on those three is the whole of what has to fire it.
--
-- INSERT and UPDATE have to become separate triggers: a column list is only
-- meaningful on UPDATE, and a BEFORE INSERT trigger cannot carry a WHEN
-- clause that references OLD. The WHEN on the UPDATE half is a second filter
-- on top of the column list -- `UPDATE OF card` fires whenever card appears
-- in the SET list even if the value is unchanged, which is exactly what the
-- sync worker does on every push that leaves the README alone.
--
-- The function itself is unchanged, so rows keep the search_vector they
-- already have and no backfill is needed.
DROP TRIGGER IF EXISTS trg_repositories_search_vector ON repositories;
DROP TRIGGER IF EXISTS trg_repositories_search_vector_insert ON repositories;
DROP TRIGGER IF EXISTS trg_repositories_search_vector_update ON repositories;

CREATE TRIGGER trg_repositories_search_vector_insert
    BEFORE INSERT ON repositories
    FOR EACH ROW EXECUTE FUNCTION repositories_search_vector_trigger();

CREATE TRIGGER trg_repositories_search_vector_update
    BEFORE UPDATE OF name, description, card ON repositories
    FOR EACH ROW
    WHEN (OLD.name IS DISTINCT FROM NEW.name
       OR OLD.description IS DISTINCT FROM NEW.description
       OR OLD.card IS DISTINCT FROM NEW.card)
    EXECUTE FUNCTION repositories_search_vector_trigger();

-- 2. `thinkingface gc` sequentially scanned the link table twice per
--    candidate.
--
-- repo_lfs_objects declares PRIMARY KEY (repo_id, oid) plus the partial
-- (repo_id, created_at) index 0002 added. Neither can serve a lookup by oid
-- alone, because oid is not the leading column of either -- and a lookup by
-- oid alone is what DeleteOrphanedLFSObject's
-- `NOT EXISTS (SELECT 1 FROM repo_lfs_objects WHERE oid = $1)` does, once per
-- candidate, while holding the lfs_objects row lock across a GCS round trip.
-- PostgreSQL then repeats the same unindexed lookup a second time, to enforce
-- the ON DELETE CASCADE foreign key when the lfs_objects row is deleted.
-- Two sequential scans of a table with one row per (repository, object) link
-- makes a collection pass over a large hub quadratic.
--
-- Migrations run inside a transaction (Store.Migrate), so this cannot be
-- CREATE INDEX CONCURRENTLY. On an existing hub it therefore blocks writes to
-- repo_lfs_objects -- pushes and LFS verifies -- for the duration of the
-- build. That is a bounded, one-off cost on a table with narrow rows; an
-- operator who cannot take it can create the index by hand with CONCURRENTLY
-- before running the migration, in which case IF NOT EXISTS makes this a
-- no-op.
CREATE INDEX IF NOT EXISTS idx_repo_lfs_objects_oid ON repo_lfs_objects (oid);
