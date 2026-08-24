-- Redirect lookups fold the namespace's case, like every other namespace
-- lookup in this schema (see 0026_namespace_name_ci_unique.sql): /Alice/foo
-- and /alice/foo are one repository before a transfer, so they must stay one
-- repository after it. from_namespace is always a canonical namespace name
-- and therefore ASCII, so a plain LOWER() fold is exact.
--
-- The PRIMARY KEY (kind, from_namespace, from_name) from 0003_repo_transfer.sql
-- cannot serve the folded predicate, so index it explicitly: the lookup runs
-- on the 404 path of every repository read, which is exactly where a
-- sequential scan must not appear. from_name stays exact -- repository names
-- are case-sensitive everywhere (see GetRepo).
CREATE INDEX IF NOT EXISTS idx_repo_redirects_from_lower
    ON repo_redirects (kind, LOWER(from_namespace), from_name);
