-- SSH public keys (postgres/0013_ssh_keys.sql,
-- docs/thinkingface-design.md §5 "Git over SSH").

CREATE TABLE IF NOT EXISTS user_ssh_keys (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title        TEXT NOT NULL,
    public_key   TEXT NOT NULL,
    fingerprint  TEXT NOT NULL UNIQUE,
    last_used_at DATETIME,
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

CREATE INDEX IF NOT EXISTS idx_user_ssh_keys_user ON user_ssh_keys (user_id, created_at DESC);
