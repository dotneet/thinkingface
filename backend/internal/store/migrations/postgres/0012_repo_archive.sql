-- Repository archive (soft, reversible): an archived repository stays fully
-- readable and downloadable but rejects every write -- git push, HF commit,
-- in-browser edit, transfer, experiment ingest. Deleting it is still allowed,
-- which is why this is a nullable timestamp on the row rather than a move to
-- a tombstone table.
--
-- archived_at doubles as the flag (NULL = active) and the audit timestamp;
-- archived_by records who flipped it, and survives that user's deletion as
-- NULL rather than dragging the repository down with it.
ALTER TABLE repositories ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;
ALTER TABLE repositories ADD COLUMN IF NOT EXISTS archived_by BIGINT REFERENCES users (id) ON DELETE SET NULL;

-- Partial: the listing filters `archived=false` far more often than it asks
-- for the archived ones, and active rows are the overwhelming majority.
CREATE INDEX IF NOT EXISTS idx_repositories_archived ON repositories (archived_at) WHERE archived_at IS NOT NULL;
