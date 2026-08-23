-- SSH public keys, used to authenticate git over SSH
-- (docs/thinkingface-design.md §5 "Git over SSH").
--
-- fingerprint is the OpenSSH "SHA256:<base64>" form and is globally unique,
-- not unique per user: the SSH server has only the offered key to go on when
-- it resolves an identity, so the same key registered by two accounts would
-- make that resolution ambiguous.

CREATE TABLE IF NOT EXISTS user_ssh_keys (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title        TEXT NOT NULL,
    public_key   TEXT NOT NULL,
    fingerprint  TEXT NOT NULL UNIQUE,
    last_used_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_user_ssh_keys_user ON user_ssh_keys (user_id, created_at DESC);
