-- See the postgres migration (0027_user_login_and_approval.sql) for the
-- reasoning, in particular why the approval column records the *pending*
-- instant rather than an approved one: NULL has to mean approved, or this
-- migration locks every existing account out of the instance.
--
-- SQLite has no ADD COLUMN IF NOT EXISTS, but every migration runs exactly
-- once (schema_migrations records it by file name), so a plain ADD COLUMN is
-- safe. DATETIME is the declared type so the driver parses the value back
-- into a UTC time.Time, the same as disabled_at in 0025.
ALTER TABLE users ADD COLUMN last_login_at DATETIME;
ALTER TABLE users ADD COLUMN approval_pending_at DATETIME;

CREATE INDEX IF NOT EXISTS idx_users_approval_pending
    ON users (approval_pending_at)
    WHERE approval_pending_at IS NOT NULL;
