-- See the postgres migration (0030) for the reasoning.
--
-- SQLite mode runs a single writer, so the two-replica race the lease closes
-- cannot happen here. The columns exist in both dialects because the queries
-- in store/jobs.go are shared: the retry backoff reads next_attempt_at on
-- every backend, and a process killed mid-sync leaves a stale 'running' row
-- under SQLite exactly as it does under Postgres.
--
-- Both columns are nullable (NULL on next_attempt_at means "due now").
-- SQLite refuses a non-constant default in ALTER TABLE ADD COLUMN, and now()
-- is rewritten to strftime(...) before the statement runs, so DEFAULT now()
-- would fail here -- see sqliteReplacer in store/sqlite.go. DATETIME is the
-- declared type so the driver parses the value back into a UTC time.Time.
--
-- SQLite has no ADD COLUMN IF NOT EXISTS, but every migration runs exactly
-- once (schema_migrations records it by file name), so a plain ADD COLUMN is
-- safe.
ALTER TABLE sync_jobs ADD COLUMN lease_expires_at DATETIME;
ALTER TABLE sync_jobs ADD COLUMN next_attempt_at  DATETIME;

CREATE INDEX IF NOT EXISTS idx_sync_jobs_due ON sync_jobs (status, next_attempt_at, id)
    WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_sync_jobs_lease ON sync_jobs (lease_expires_at)
    WHERE status = 'running';
