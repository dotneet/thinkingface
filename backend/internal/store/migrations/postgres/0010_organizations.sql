-- Organisations (docs/dev/organization-design.md §6.1): profile columns and
-- policies on the namespace row, membership bookkeeping, and the per-org
-- audit log.
--
-- The columns exist for user namespaces too but are never used there (NULL /
-- default), which keeps the namespace table a single name space rather than
-- splitting orgs into a table of their own.

ALTER TABLE namespaces ADD COLUMN IF NOT EXISTS display_name           TEXT NOT NULL DEFAULT '';
ALTER TABLE namespaces ADD COLUMN IF NOT EXISTS description            TEXT NOT NULL DEFAULT '';
ALTER TABLE namespaces ADD COLUMN IF NOT EXISTS website                TEXT NOT NULL DEFAULT '';
ALTER TABLE namespaces ADD COLUMN IF NOT EXISTS avatar_url             TEXT NOT NULL DEFAULT '';
ALTER TABLE namespaces ADD COLUMN IF NOT EXISTS members_visibility     TEXT NOT NULL DEFAULT 'members'
    CHECK (members_visibility IN ('members', 'public'));
ALTER TABLE namespaces ADD COLUMN IF NOT EXISTS created_by             BIGINT REFERENCES users (id) ON DELETE SET NULL;
ALTER TABLE namespaces ADD COLUMN IF NOT EXISTS updated_at             TIMESTAMPTZ NOT NULL DEFAULT now();

ALTER TABLE org_members ADD COLUMN IF NOT EXISTS added_by   BIGINT REFERENCES users (id) ON DELETE SET NULL;
ALTER TABLE org_members ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE org_members ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE INDEX IF NOT EXISTS org_members_user_idx ON org_members (user_id);

-- An organisation must not depend on its founder: guarantee the founder's
-- admin row, then drop owner_user_id. That way (a) removing the founder from
-- org_members really removes their power, and (b) deleting the founder's
-- account no longer cascades the whole organisation away.
INSERT INTO org_members (namespace_id, user_id, role)
    SELECT id, owner_user_id, 'admin' FROM namespaces
    WHERE kind = 'org' AND owner_user_id IS NOT NULL
ON CONFLICT (namespace_id, user_id) DO NOTHING;

UPDATE namespaces SET created_by = owner_user_id WHERE kind = 'org' AND created_by IS NULL;
UPDATE namespaces SET owner_user_id = NULL       WHERE kind = 'org';

CREATE TABLE IF NOT EXISTS org_audit_log (
    id             BIGSERIAL PRIMARY KEY,
    namespace_id   BIGINT NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    actor_user_id  BIGINT REFERENCES users (id) ON DELETE SET NULL,
    -- Denormalised so the line still reads after the account is deleted.
    actor_name     TEXT NOT NULL,
    action         TEXT NOT NULL,
    target_user_id BIGINT REFERENCES users (id) ON DELETE SET NULL,
    -- Target username, repository full name, or webhook URL, per action.
    target_name    TEXT NOT NULL DEFAULT '',
    details        JSONB NOT NULL DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS org_audit_log_ns_idx ON org_audit_log (namespace_id, id DESC);
