-- The blobs/ layer's deletion ledger, and the index the collector's
-- per-sha reference re-check needs.
--
-- `thinkingface gc`'s blob pass used to decide from two snapshots -- a bucket
-- listing and one SELECT of every referenced sha -- and then delete. Nothing
-- linked the decision to the writers, so a push that referenced a sha in
-- between lost its bytes:
--
--   1. blob X is orphaned and older than the 24h grace
--   2. gc reads the reference set: X is not in it
--   3. a push to another repository commits the same content. PublishBlob
--      finds X already at its key and skips the write, so the object's
--      Updated timestamp does not move and the grace cannot see it; then
--      ReplaceRepoFiles commits a row naming X
--   4. gc deletes X
--   5. every later push to that ref skips X as well, because
--      ListIndexedBlobSHAs says it is already published -- so resolve 404s
--      on that file until somebody runs `thinkingface resync`
--
-- The lfs/ layer has no such window: its collectors claim the oid under the
-- same lfs_objects row lock the upload paths take, and only then delete. The
-- blobs/ layer has no row to lock -- a blob is recorded nowhere at the moment
-- it is written -- so this table is the row.
--
-- It is deliberately *not* a mirror of lfs_objects. Registering every blob
-- would put a write on the push path for every file of every revision, for a
-- layer whose whole point is that a key is derivable from the content. What
-- is recorded instead is only the rare event: the collector's intent to
-- remove a sha. Two things then follow from one row.
--
--   * gc commits the row **before** it touches storage, and takes it FOR
--     UPDATE while it re-checks repo_files and deletes. A push's repair pass
--     (store.RepairDeletedBlobs, run at the end of the sync pipeline) takes
--     the same row, so the two serialise: whichever runs first, the other
--     sees the outcome -- gc finds the reference and refuses, or the repair
--     finds the ledger row and re-publishes the bytes.
--   * a crash between the two leaves a row for bytes that may or may not be
--     there, which is exactly the state the repair pass is written to fix:
--     PublishBlob is idempotent, so a re-publish of an object that survived
--     costs one Stat.
--
-- Rows do not accumulate. The repair pass removes the ones it has answered,
-- and gc prunes the rest once no repo_files row names the sha any more
-- (store.PruneBlobDeletions) -- which is the ordinary case, since a sha gc
-- successfully collected is by definition referenced by nothing.
CREATE TABLE IF NOT EXISTS blob_deletions (
    blob_sha   TEXT PRIMARY KEY,
    deleted_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- repo_files is only indexed by (repo_id, ref, path), so asking "does any
-- revision still name this sha" -- which the collector now does once per
-- candidate, while holding a ledger row across a storage round trip -- was a
-- sequential scan of the whole file index per blob. Partial, because every
-- query that asks it also carries `lfs_oid IS NULL`: an LFS file's blob is
-- the pointer text and never reaches blobs/ at all, so those rows are not
-- part of this layer's reference count (see store.ListReferencedBlobSHAs).
CREATE INDEX IF NOT EXISTS idx_repo_files_blob_sha
    ON repo_files (blob_sha) WHERE lfs_oid IS NULL;
