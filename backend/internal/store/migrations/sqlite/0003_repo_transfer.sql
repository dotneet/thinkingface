-- Repository transfer (postgres/0009_repo_transfer.sql,
-- docs/repo-transfer-design.md §4, §7).

CREATE TABLE IF NOT EXISTS repo_redirects (
    kind           TEXT    NOT NULL CHECK (kind IN ('dataset', 'model')),
    from_namespace TEXT    NOT NULL,
    from_name      TEXT    NOT NULL,
    repo_id        INTEGER NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    PRIMARY KEY (kind, from_namespace, from_name)
);

CREATE INDEX IF NOT EXISTS idx_repo_redirects_repo ON repo_redirects (repo_id);

CREATE TABLE IF NOT EXISTS repo_transfers (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id           INTEGER NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    from_namespace_id INTEGER NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    from_name         TEXT    NOT NULL,
    to_namespace_id   INTEGER NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    to_name           TEXT    NOT NULL,
    requested_by      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    status            TEXT    NOT NULL CHECK (status IN ('pending', 'accepted', 'rejected', 'cancelled', 'expired')),
    decided_by        INTEGER REFERENCES users (id) ON DELETE SET NULL,
    decided_at        DATETIME,
    expires_at        DATETIME NOT NULL,
    created_at        DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_repo_transfers_one_pending ON repo_transfers (repo_id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_repo_transfers_to_pending ON repo_transfers (to_namespace_id) WHERE status = 'pending';

ALTER TABLE sync_jobs ADD COLUMN kind    TEXT NOT NULL DEFAULT 'push';
ALTER TABLE sync_jobs ADD COLUMN payload TEXT NOT NULL DEFAULT '{}';
