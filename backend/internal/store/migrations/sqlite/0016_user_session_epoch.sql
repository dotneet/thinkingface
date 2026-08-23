-- Session revocation handle (postgres/0022_user_session_epoch.sql).
ALTER TABLE users ADD COLUMN session_epoch INTEGER NOT NULL DEFAULT 0;
