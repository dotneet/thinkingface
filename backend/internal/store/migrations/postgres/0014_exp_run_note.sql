-- Free-form note on a run: the Markdown a person writes on the run detail page
-- ("swept lr from the 0.1 run, diverged at step 4k"). Like tags / archived /
-- is_baseline (postgres/0002_run_annotations.sql) it is hand-maintained, so it
-- must survive re-indexing: UpsertExpRun never lists the column, which leaves
-- the stored value alone on conflict.

ALTER TABLE exp_runs ADD COLUMN IF NOT EXISTS note TEXT NOT NULL DEFAULT '';
