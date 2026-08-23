-- Repository transfer (docs/repo-transfer-design.md §4, §7): moving a
-- repository to another namespace (and optionally renaming it) never moves
-- git/LFS/WAL data, only repositories.namespace_id/name plus the bookkeeping
-- below.

-- Every former (kind, namespace, name) a repository used to be reachable
-- at, so a request for the old name resolves to the current row instead of
-- 404ing. A name is claimed by at most one row: a transfer upserts it, and
-- creating a new repository at a redirected-from name deletes it (the new
-- repository wins -- docs/repo-transfer-design.md §5 "conflicts").
CREATE TABLE IF NOT EXISTS repo_redirects (
    kind           TEXT   NOT NULL CHECK (kind IN ('dataset', 'model')),
    from_namespace TEXT   NOT NULL,
    from_name      TEXT   NOT NULL,
    repo_id        BIGINT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, from_namespace, from_name)
);

CREATE INDEX IF NOT EXISTS idx_repo_redirects_repo ON repo_redirects (repo_id);

-- Transfer requests/records. An immediate move (actor has write access to
-- both sides) still leaves one 'accepted' row for audit; a move to a
-- namespace the actor cannot write to starts 'pending' and needs the target
-- namespace's owner/admin to accept it (docs/repo-transfer-design.md §7.2).
CREATE TABLE IF NOT EXISTS repo_transfers (
    id                BIGSERIAL PRIMARY KEY,
    repo_id           BIGINT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    from_namespace_id BIGINT NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    -- The repository's name at request time, kept so the row still makes
    -- sense after the repository has since moved again.
    from_name         TEXT   NOT NULL,
    to_namespace_id   BIGINT NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    to_name           TEXT   NOT NULL,          -- transfer may rename at the same time; usually equal to from_name
    requested_by      BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    status            TEXT   NOT NULL CHECK (status IN ('pending', 'accepted', 'rejected', 'cancelled', 'expired')),
    decided_by        BIGINT REFERENCES users (id) ON DELETE SET NULL,
    decided_at        TIMESTAMPTZ,
    expires_at        TIMESTAMPTZ NOT NULL,     -- pending validity window (7 days, set by the caller)
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- At most one pending transfer per repository at a time.
CREATE UNIQUE INDEX IF NOT EXISTS idx_repo_transfers_one_pending ON repo_transfers (repo_id) WHERE status = 'pending';
-- Fast lookup of "what is pending that I could accept" by target namespace.
CREATE INDEX IF NOT EXISTS idx_repo_transfers_to_pending ON repo_transfers (to_namespace_id) WHERE status = 'pending';

-- sync_jobs grows a job kind: 'push' (existing behaviour, the default) or
-- 'relocate_exports' (copies exports/ to the new namespace/name and deletes
-- the old prefix after a transfer -- docs/repo-transfer-design.md §10).
-- payload carries kind-specific parameters as JSON.
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS kind    TEXT  NOT NULL DEFAULT 'push';
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS payload JSONB NOT NULL DEFAULT '{}';
