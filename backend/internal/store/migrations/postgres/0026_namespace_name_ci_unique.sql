-- Namespace names must be unique regardless of case: "Alice" and "alice"
-- are the same identifier everywhere it is used downstream (the /{ns}/{name}
-- route, the HF-compatible /datasets/{ns}/{name} shape, git remotes, LFS key
-- layout). 0001_init.sql only enforced exact-match uniqueness, which let
-- "alice" and "Alice" coexist as two different accounts/orgs
-- (docs/thinkingface-design.md §10).
--
-- namespaces.name is always ASCII: backend/internal/api/repos.go's nameRe
-- restricts it to [A-Za-z0-9._-], so a plain SQL LOWER() fold is exact and
-- needs no locale/ICU support.
--
-- The pre-existing UNIQUE(name) constraint from 0001_init.sql is left in
-- place rather than dropped: it is now implied by this index (two rows with
-- the same exact name also share the same lowercased name), so it is inert
-- rather than wrong, and dropping a plain-column UNIQUE constraint on
-- Postgres is more churn than the redundancy is worth here.
CREATE UNIQUE INDEX IF NOT EXISTS idx_namespaces_name_lower ON namespaces (LOWER(name));

-- users.username carries its own exact-match UNIQUE from 0001_init.sql, and
-- CreateUser writes it alongside the namespace row in one transaction, so the
-- index above already keeps two users differing only by case from existing.
-- Indexing the folded username as well makes that guarantee local to the
-- users table, so the case-insensitive login lookup
-- (store.GetUserByUsername) can never match more than one row even if a user
-- row is ever created without its namespace.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username_lower ON users (LOWER(username));
