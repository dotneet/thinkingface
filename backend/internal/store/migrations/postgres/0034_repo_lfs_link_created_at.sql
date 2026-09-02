-- When a repository's link to an LFS object was made.
--
-- repo_lfs_objects is three things at once: the entitlement that authorises a
-- download, the reference count `thinkingface gc` collects against, and --
-- through UsageByRepo -- the numerator of every storage quota. Nothing ever
-- deleted a row, so all three grew monotonically: deleting a 10 GiB file and
-- pushing again freed nothing, and a namespace that reached its quota could
-- only be rescued by deleting the whole repository. A `tf up` interrupted
-- after the LFS transfer but before the commit left tens of gigabytes charged
-- to a repository holding no files at all.
--
-- The links are now reconciled after each successful sync
-- (Store.PruneRepoLFSLinks), which needs to tell "no longer referenced" from
-- "not referenced *yet*": an object is uploaded, promoted and linked well
-- before the commit that names it arrives, and for a large dataset that gap
-- is hours. This column dates the link so only settled ones are considered.
--
-- (0036 narrowed what that reconciliation may release. Reading repo_files --
-- the ref tips -- as the reference set also released everything named only by
-- history, which broke checkout and resolve at older revisions. See 0036.)
--
-- Existing rows are dated at migration time rather than backdated, so the
-- accumulated garbage becomes collectable one grace period from now instead
-- of immediately -- an upload that is in flight while this runs keeps its
-- link.
ALTER TABLE repo_lfs_objects
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- The prune scans one repository's links by age.
CREATE INDEX IF NOT EXISTS idx_repo_lfs_objects_repo_created
    ON repo_lfs_objects (repo_id, created_at);
