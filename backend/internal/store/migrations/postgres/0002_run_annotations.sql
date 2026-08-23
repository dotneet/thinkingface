-- Run annotations: the hand-maintained metadata a person attaches to a run
-- while comparing experiments. None of it comes from the ingest path or the
-- parquet export, so the columns must survive re-indexing: UpsertExpRun never
-- lists them, which leaves the stored values alone on conflict.

ALTER TABLE exp_runs ADD COLUMN IF NOT EXISTS tags TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE exp_runs ADD COLUMN IF NOT EXISTS archived BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE exp_runs ADD COLUMN IF NOT EXISTS is_baseline BOOLEAN NOT NULL DEFAULT FALSE;

-- The run list filters archived runs out by default, so the flag joins the
-- project key in the index the listing already walks.
CREATE INDEX IF NOT EXISTS idx_exp_runs_project_archived ON exp_runs (project_id, archived);
