-- Account suspension: the offboarding switch.
--
-- Resetting a password deliberately does not revoke access tokens (that
-- decision is documented in api-contract.md §1.3), and SSH keys were never
-- touched by it either. The consequence was that an administrator had no way
-- to actually cut somebody off: the only lever was a password reset, after
-- which the departed account's tf_ tokens and registered public keys kept
-- working for `git push` and every huggingface_hub write.
--
-- disabled_at is checked by every identity path -- session cookie, password,
-- bearer token and SSH public key -- so setting it stops all of them at once
-- without deleting anything. Deletion is a separate, harder problem (what
-- happens to the namespace and its repositories) and is deliberately not
-- solved here; suspension is what an offboarding actually needs.
ALTER TABLE users ADD COLUMN IF NOT EXISTS disabled_at TIMESTAMPTZ;

-- disabled_by records who suspended the account, for the same reason every
-- other administrative action names its actor.
ALTER TABLE users ADD COLUMN IF NOT EXISTS disabled_by BIGINT REFERENCES users (id) ON DELETE SET NULL;
