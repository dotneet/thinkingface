-- When a repository's link to an LFS object was made, and when a commit was
-- first observed to name it.
--
-- repo_lfs_objects is three things at once: the entitlement that authorises a
-- download (store.RepoHasLFSObject, read by `resolve` and by the LFS batch's
-- download branch), the reference count `thinkingface gc` collects against,
-- and -- through UsageByRepo -- the numerator of every storage quota. Nothing
-- ever deleted a row, so all three grew monotonically: a `tf up` interrupted
-- after the LFS transfer but before the commit left tens of gigabytes charged
-- to a repository holding no files at all, and a namespace that reached its
-- quota that way could only be rescued by deleting the whole repository.
--
-- Reclaiming those links needs to tell "never referenced" from "not
-- referenced *yet*": an object is uploaded, promoted and linked well before
-- the commit that names it arrives, and for a large dataset that gap is
-- hours. created_at dates the link so only settled ones are considered.
--
-- committed_at is what bounds the rule. Reading repo_files -- one row per file
-- of each *ref tip* -- as the reference set would release every object named
-- only by history: an LFS file deleted on the tip, or one that only ever
-- existed on a commit further back. The link is not a cache, so releasing it
-- breaks `git checkout <old sha>`, `git lfs fetch --all` and resolve at any
-- historical revision. The blobs/ layer can be collected on the tip rule
-- because it is a publishing copy and the bare repository still holds the git
-- object; for LFS the bucket is the only copy there is.
--
-- So the rule is the one git and the HuggingFace Hub both use: an object any
-- commit ever named stays linked for as long as the repository exists. NULL
-- committed_at means "uploaded, never named by a commit", and that is the
-- only thing Store.PruneRepoLFSLinks reclaims.
ALTER TABLE repo_lfs_objects ADD COLUMN created_at DATETIME;
ALTER TABLE repo_lfs_objects ADD COLUMN committed_at DATETIME;

-- Existing rows are dated and stamped rather than backdated or left NULL.
-- This migration cannot tell which of them a commit named, and the safe
-- reading of "unknown" is "keep": a link wrongly kept costs an over-counted
-- namespace, one wrongly dropped costs a 404 on content that is still in the
-- bucket. Abandoned uploads made before this runs are therefore not
-- collectable; the next push that names them, or an operator, is what clears
-- them.
UPDATE repo_lfs_objects SET created_at = now() WHERE created_at IS NULL;
UPDATE repo_lfs_objects SET committed_at = now() WHERE committed_at IS NULL;

-- The prune scans one repository's never-committed links by age, so the
-- partial index is the whole of what it reads.
CREATE INDEX IF NOT EXISTS idx_repo_lfs_objects_uncommitted
    ON repo_lfs_objects (repo_id, created_at)
    WHERE committed_at IS NULL;
