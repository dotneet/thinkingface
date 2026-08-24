-- See the postgres migration (0031) for the reasoning.
--
-- SQLite has no ADD COLUMN IF NOT EXISTS, but every migration runs exactly
-- once (schema_migrations records it by file name), so a plain ADD COLUMN is
-- safe. DATETIME is the declared type so the driver parses the value back
-- into a UTC time.Time.
ALTER TABLE users ADD COLUMN disabled_at DATETIME;
ALTER TABLE users ADD COLUMN disabled_by INTEGER REFERENCES users (id) ON DELETE SET NULL;
