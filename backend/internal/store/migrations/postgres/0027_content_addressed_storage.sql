-- The exports/ layer is gone. Object keys no longer carry a namespace, a
-- repository name or a ref: LFS bytes live at lfs/{oid[0:2]}/{oid[2:4]}/{oid}
-- and plain git blobs at blobs/{sha[0:2]}/{sha[2:4]}/{sha}, both immutable and
-- shared instance-wide. Transferring, renaming or deleting a repository now
-- moves nothing in the bucket.
--
-- Three pieces of the old design disappear with it.

-- 1. lfs_objects.storage_key recorded which of the two homes an object had
--    been moved to. There is only one home now and it is derived from the oid,
--    so the column (and the reverse-lookup index that made "who lives at this
--    key?" answerable) is dead weight.
DROP INDEX IF EXISTS idx_lfs_objects_storage_key;
ALTER TABLE lfs_objects DROP COLUMN IF EXISTS storage_key;

-- 2. storage_reclaim_jobs existed to take down a deleted repository's exports/
--    tree after the row was gone. Deleting a repository no longer touches
--    storage at all; `thinkingface gc` reclaims unreferenced objects instead.
DROP TABLE IF EXISTS storage_reclaim_jobs;

-- 3. relocate_exports jobs mirrored exports/ to a transferred repository's new
--    name. Any still queued would run a pipeline that no longer exists.
DELETE FROM sync_jobs WHERE kind = 'relocate_exports';
