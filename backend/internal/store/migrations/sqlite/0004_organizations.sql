-- Organisations (postgres/0010_organizations.sql,
-- docs/dev/organization-design.md §6.1).
--
-- SQLite cannot attach a NOT NULL constraint to a column added with a
-- non-constant default (CURRENT_TIMESTAMP / a parenthesised expression), so
-- the three timestamp columns are added nullable and backfilled here.
-- Readers COALESCE them onto created_at, and every write sets them
-- explicitly, so no NULL is ever observed.

ALTER TABLE namespaces ADD COLUMN display_name           TEXT NOT NULL DEFAULT '';
ALTER TABLE namespaces ADD COLUMN description            TEXT NOT NULL DEFAULT '';
ALTER TABLE namespaces ADD COLUMN website                TEXT NOT NULL DEFAULT '';
ALTER TABLE namespaces ADD COLUMN avatar_url             TEXT NOT NULL DEFAULT '';
ALTER TABLE namespaces ADD COLUMN members_visibility     TEXT NOT NULL DEFAULT 'members'
    CHECK (members_visibility IN ('members', 'public'));
ALTER TABLE namespaces ADD COLUMN created_by             INTEGER REFERENCES users (id) ON DELETE SET NULL;
ALTER TABLE namespaces ADD COLUMN updated_at             DATETIME;

UPDATE namespaces SET updated_at = created_at WHERE updated_at IS NULL;

ALTER TABLE org_members ADD COLUMN added_by   INTEGER REFERENCES users (id) ON DELETE SET NULL;
ALTER TABLE org_members ADD COLUMN created_at DATETIME;
ALTER TABLE org_members ADD COLUMN updated_at DATETIME;

UPDATE org_members SET created_at = strftime('%Y-%m-%d %H:%M:%f','now') WHERE created_at IS NULL;
UPDATE org_members SET updated_at = created_at WHERE updated_at IS NULL;

CREATE INDEX IF NOT EXISTS org_members_user_idx ON org_members (user_id);

-- An organisation must not depend on its founder (see the Postgres file).
INSERT OR IGNORE INTO org_members (namespace_id, user_id, role, created_at, updated_at)
    SELECT id, owner_user_id, 'admin',
           strftime('%Y-%m-%d %H:%M:%f','now'), strftime('%Y-%m-%d %H:%M:%f','now')
    FROM namespaces
    WHERE kind = 'org' AND owner_user_id IS NOT NULL;

UPDATE namespaces SET created_by = owner_user_id WHERE kind = 'org' AND created_by IS NULL;
UPDATE namespaces SET owner_user_id = NULL       WHERE kind = 'org';

CREATE TABLE IF NOT EXISTS org_audit_log (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    namespace_id   INTEGER NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    actor_user_id  INTEGER REFERENCES users (id) ON DELETE SET NULL,
    actor_name     TEXT NOT NULL,
    action         TEXT NOT NULL,
    target_user_id INTEGER REFERENCES users (id) ON DELETE SET NULL,
    target_name    TEXT NOT NULL DEFAULT '',
    details        TEXT NOT NULL DEFAULT '{}',
    created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

CREATE INDEX IF NOT EXISTS org_audit_log_ns_idx ON org_audit_log (namespace_id, id DESC);
