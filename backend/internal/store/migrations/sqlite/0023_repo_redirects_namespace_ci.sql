-- See postgres/0029_repo_redirects_namespace_ci.sql for the rationale.
-- SQLite supports expression indexes, so this is unmodified from the
-- Postgres side.
CREATE INDEX IF NOT EXISTS idx_repo_redirects_from_lower
    ON repo_redirects (kind, LOWER(from_namespace), from_name);
