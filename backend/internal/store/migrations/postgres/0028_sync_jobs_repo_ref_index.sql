-- ClaimSyncJob now refuses a job whose repo+ref another worker is already
-- syncing, which it decides with a NOT EXISTS over this table (see the comment
-- on the function). The existing idx_sync_jobs_pending covers picking the next
-- pending row; this one covers the sibling lookup, so the added clause does not
-- turn every claim into a sequential scan of the queue.
CREATE INDEX IF NOT EXISTS idx_sync_jobs_repo_ref_status ON sync_jobs (repo_id, ref, status);
