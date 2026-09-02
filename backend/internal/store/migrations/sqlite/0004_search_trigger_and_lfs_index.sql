-- Two writes that had no business being expensive: a file download, and the
-- LFS garbage collector's orphan check. The PostgreSQL file of the same name
-- makes the same two changes in that dialect's terms.

-- 1. The full-text index was rebuilt on every file download.
--
-- 0001_init.sql attached repositories_fts_au to `AFTER UPDATE ON
-- repositories` with no column list and no WHEN guard, so *every* UPDATE on
-- the table ran a DELETE followed by an INSERT into the FTS5 index. The
-- statement that dominates writes to this table is Store.IncrementDownloads
-- -- `UPDATE repositories SET downloads = downloads + 1`, issued from a
-- detached goroutine on every single resolve -- and it cannot change the
-- index by construction. SQLite mode is single-writer, so those rebuilds do
-- not merely waste work: every unrelated push queues behind them on the one
-- writer connection.
--
-- The index reads exactly three columns: name, description and card (tags,
-- license, pipeline_tag and task_categories are all keys of the card), so
-- `UPDATE OF` on those three is the whole of what has to fire it. The WHEN
-- guard is a second filter on top of that -- `UPDATE OF card` fires whenever
-- card appears in the SET list even if the value is unchanged, which is
-- exactly what the sync worker does on every push that leaves the README
-- alone. (`IS NOT` is SQLite's null-safe inequality, the counterpart of
-- PostgreSQL's IS DISTINCT FROM.)
--
-- The trigger body is copied unchanged from 0001_init.sql; only the event and
-- the guard differ, so existing index rows stay correct and no rebuild is
-- needed.
DROP TRIGGER IF EXISTS repositories_fts_au;

CREATE TRIGGER repositories_fts_au
AFTER UPDATE OF name, description, card ON repositories
WHEN old.name IS NOT new.name
  OR old.description IS NOT new.description
  OR old.card IS NOT new.card
BEGIN
    DELETE FROM repositories_fts WHERE rowid = old.id;
    INSERT INTO repositories_fts
        (rowid, name, tags, description, card_description, short_description, summary, license, pipeline_tag, task_categories)
    VALUES (
        new.id,
        new.name,
        CASE json_type(new.card, '$.tags')
            WHEN 'array' THEN (SELECT coalesce(group_concat(value, ' '), '') FROM json_each(new.card, '$.tags'))
            WHEN 'text' THEN new.card ->> '$.tags'
            ELSE ''
        END,
        new.description,
        coalesce(new.card ->> '$.description', ''),
        coalesce(new.card ->> '$.short_description', ''),
        coalesce(new.card ->> '$.summary', ''),
        coalesce(new.card ->> '$.license', ''),
        CASE json_type(new.card, '$.pipeline_tag')
            WHEN 'array' THEN (SELECT coalesce(group_concat(value, ' '), '') FROM json_each(new.card, '$.pipeline_tag'))
            WHEN 'text' THEN new.card ->> '$.pipeline_tag'
            ELSE ''
        END,
        CASE json_type(new.card, '$.task_categories')
            WHEN 'array' THEN (SELECT coalesce(group_concat(value, ' '), '') FROM json_each(new.card, '$.task_categories'))
            WHEN 'text' THEN new.card ->> '$.task_categories'
            ELSE ''
        END
    );
END;

-- 2. `thinkingface gc` sequentially scanned the link table twice per
--    candidate.
--
-- repo_lfs_objects declares PRIMARY KEY (repo_id, oid) plus the partial
-- (repo_id, created_at) index 0002 added. Neither can serve a lookup by oid
-- alone, because oid is not the leading column of either -- and a lookup by
-- oid alone is what DeleteOrphanedLFSObject's
-- `NOT EXISTS (SELECT 1 FROM repo_lfs_objects WHERE oid = $1)` does, once per
-- candidate, while holding the write transaction across a GCS round trip.
-- The ON DELETE CASCADE foreign key from repo_lfs_objects to lfs_objects
-- needs the same lookup again when the parent row goes.
CREATE INDEX IF NOT EXISTS idx_repo_lfs_objects_oid ON repo_lfs_objects (oid);
