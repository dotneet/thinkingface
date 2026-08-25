-- Two things an instance needs before it can be run by more than one person:
-- a way to tell a dormant account from a live one, and a waiting room for
-- self-registrations.
--
-- last_login_at is moved only when a password mints a session (handleLogin).
-- Access tokens and SSH keys already carry their own last-used timestamps and
-- deliberately do not touch this one: the question it answers is "is anybody
-- still using this account", and an automation's token running nightly is the
-- wrong signal for that. NULL means the account has never signed in, which is
-- also what every account that predates this column reads as -- honest, since
-- nothing recorded it before now.
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_login_at TIMESTAMPTZ;

-- approval_pending_at is when a self-registration was put in the waiting room
-- (TF_SIGNUP_REQUIRE_APPROVAL), NULL once a site administrator has admitted
-- it. It is deliberately the *pending* instant rather than an approved_at, so
-- that NULL -- the value every existing row gets, and the default every
-- INSERT that says nothing gets -- means approved. The reverse spelling would
-- have every account on the instance locked out the moment this migration
-- ran, and every account created by an administrator locked out afterwards.
--
-- A pending account authenticates on no path at all, exactly like disabled_at
-- (0031): both predicates live at the single exit of credential resolution
-- and in the two statements that resolve a credential outside it
-- (LookupToken, LookupSSHKey).
ALTER TABLE users ADD COLUMN IF NOT EXISTS approval_pending_at TIMESTAMPTZ;

-- The account directory's default sort is by username, but the waiting room
-- is read as "who is pending", which is a tiny fraction of the table on any
-- instance where it is used at all.
CREATE INDEX IF NOT EXISTS idx_users_approval_pending
    ON users (approval_pending_at)
    WHERE approval_pending_at IS NOT NULL;
