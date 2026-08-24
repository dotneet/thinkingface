-- Ownership and retry pacing for the sync queue.
--
-- Before this, a worker claimed a job by flipping it to 'running' and nothing
-- recorded whether that claim was still alive. RequeueRunningJobs then reset
-- every 'running' row at startup, so a second replica booting while the first
-- was mid-sync stole its job: both then walked their own OldSHA..NewSHA diff
-- and published two disjoint sets of blobs, leaving a file touched only by the
-- job that finished first with no blobs/{sha} object and nothing to republish
-- it (see the comment on ClaimSyncJob). infra defaults api_max_instances to 4,
-- so this was reachable on any ordinary scale-up, not just a redeploy.
--
-- lease_expires_at makes the claim explicit: a worker holds a job only until
-- its lease runs out and extends it while it is genuinely working, so the
-- sweeper can tell a crashed claim from a live one and requeue only the dead.
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ;

-- next_attempt_at paces retries. A failing job used to go straight back to
-- 'pending' and be reclaimed on the next tick, so all three attempts burned
-- within seconds and the row parked as 'failed' long before a transient GCS
-- 5xx had any chance to clear.
--
-- Nullable, with NULL meaning "due now", rather than DEFAULT now(): SQLite
-- rejects a non-constant default in ALTER TABLE ADD COLUMN, and now() is
-- rewritten to strftime(...) there. Keeping both dialects nullable lets
-- store/jobs.go run one shared query instead of branching per backend.
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ;

-- idx_sync_jobs_pending covers picking the next pending row by id; the claim
-- now also filters on next_attempt_at and the sweeper scans running rows by
-- lease, so each gets an index rather than turning into a queue scan.
CREATE INDEX IF NOT EXISTS idx_sync_jobs_due ON sync_jobs (status, next_attempt_at, id)
    WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_sync_jobs_lease ON sync_jobs (lease_expires_at)
    WHERE status = 'running';
