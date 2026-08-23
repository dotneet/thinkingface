-- Session revocation handle. tf_session is a stateless signed value, so
-- without a server-side counter an issued cookie stays valid for its whole
-- TTL no matter what the user does. Logout and password changes increment
-- this, and a cookie whose epoch no longer matches is rejected.

ALTER TABLE users ADD COLUMN IF NOT EXISTS session_epoch BIGINT NOT NULL DEFAULT 0;
