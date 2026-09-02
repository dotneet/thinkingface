-- When a commit of this repository was last observed to name the object.
--
-- 0034 made repo_lfs_objects prunable and reconciled it against repo_files.
-- repo_files holds one row per file of each *ref tip*, so that rule released
-- every object named only by history: an LFS file deleted on the tip, or one
-- that only ever existed on a commit further back. The link is not a cache --
-- it is the entitlement `resolve` and the LFS batch's download branch check
-- (store.RepoHasLFSObject) -- so releasing it broke `git checkout <old sha>`,
-- `git lfs fetch --all` and resolve at any historical revision, one grace
-- period after the file was deleted. The blobs/ layer can be collected on the
-- tip rule because it is a publishing copy and the bare repository still holds
-- the git object; for LFS the bucket is the only copy there is.
--
-- So the rule is now the one git and the HuggingFace Hub both use: an object
-- any commit ever named stays linked for as long as the repository exists.
-- This column records that. NULL means "uploaded, never named by a commit" --
-- an LFS transfer that completed and whose commit never arrived -- and that
-- is the only thing the prune reclaims.
ALTER TABLE repo_lfs_objects
    ADD COLUMN IF NOT EXISTS committed_at TIMESTAMPTZ;

-- Existing rows are stamped rather than left NULL. This migration cannot tell
-- which of them a commit named, and the safe reading of "unknown" is "keep":
-- a link wrongly kept costs an over-counted namespace, one wrongly dropped
-- costs a 404 on content that is still in the bucket. Abandoned uploads made
-- before this runs are therefore not collectable; the next push that names
-- them, or an operator, is what clears them.
UPDATE repo_lfs_objects SET committed_at = now() WHERE committed_at IS NULL;

-- The prune scans one repository's never-committed links by age, so the
-- partial index is the whole of what it reads.
CREATE INDEX IF NOT EXISTS idx_repo_lfs_objects_uncommitted
    ON repo_lfs_objects (repo_id, created_at)
    WHERE committed_at IS NULL;
