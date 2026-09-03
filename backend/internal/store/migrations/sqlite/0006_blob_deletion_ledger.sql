-- The blobs/ layer's deletion ledger, and the index the collector's
-- per-sha reference re-check needs. The Postgres copy of this file carries
-- the full reasoning; the short version is that `thinkingface gc` decided
-- from two snapshots and then deleted, so a push that referenced an
-- otherwise-orphaned sha in between lost its bytes permanently -- permanently
-- because the next push skips a sha the ref's index already names.
--
-- The lfs/ collectors claim an oid under the lfs_objects row lock the upload
-- paths take. blobs/ has no row to lock, because nothing records a blob when
-- it is written, so this table is the row: gc commits the intent before it
-- removes anything and holds it while it re-checks repo_files, and the sync
-- pipeline's repair pass (store.RepairDeletedBlobs) takes the same row and
-- re-publishes whatever the collector took.
--
-- SQLite never runs the collector at all (backend/entrypoint.sh refuses `gc`
-- against a Litestream-restored snapshot), so on this engine the table only
-- ever has to exist and stay schema-identical to the Postgres one. The
-- serialisation argument is Postgres': there are no row locks here, and none
-- are needed while nothing writes the table.
CREATE TABLE IF NOT EXISTS blob_deletions (
    blob_sha   TEXT PRIMARY KEY,
    deleted_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

-- repo_files is only indexed by (repo_id, ref, path), so "does any revision
-- still name this sha" scanned the whole file index per candidate blob.
-- Partial, because every query that asks it also carries `lfs_oid IS NULL`:
-- an LFS file's blob is the pointer text and never reaches blobs/.
CREATE INDEX IF NOT EXISTS idx_repo_files_blob_sha
    ON repo_files (blob_sha) WHERE lfs_oid IS NULL;
