-- See postgres/0026_namespace_name_ci_unique.sql for the full rationale:
-- namespace names must be unique case-insensitively, and are always ASCII
-- (backend/internal/api/repos.go nameRe), so a plain LOWER() expression
-- index is exact on both engines.
--
-- SQLite supports expression indexes, so this is unmodified from the
-- Postgres side. The column-level UNIQUE(name) constraint from 0001_init.sql
-- is left in place for the same reason as Postgres: it is now implied by
-- this index, and SQLite cannot drop a column-level UNIQUE constraint
-- without rebuilding the table, which is not worth it for an inert
-- constraint.
CREATE UNIQUE INDEX IF NOT EXISTS idx_namespaces_name_lower ON namespaces (LOWER(name));

-- users.username carries its own exact-match UNIQUE from 0001_init.sql, and
-- CreateUser writes it alongside the namespace row in one transaction, so the
-- index above already keeps two users differing only by case from existing.
-- Indexing the folded username as well makes that guarantee local to the
-- users table, so the case-insensitive login lookup
-- (store.GetUserByUsername) can never match more than one row even if a user
-- row is ever created without its namespace.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username_lower ON users (LOWER(username));
