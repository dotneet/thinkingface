-- Run summaries beyond the last value, heartbeat-aware staleness, and metric
-- goals (docs/dev/agent-features.md §2.1, §2.2, §2.6).
--
-- summary_min / summary_max sit next to summary (the last value per metric)
-- and are maintained the same way: merged at ingest -- the smaller / larger of
-- the stored value and the batch -- and recomputed from the parquet by the
-- indexer. They are what lets a run listing sort by "best validation loss"
-- without reading every run's series.
--
-- heartbeat_secs is how often the logging client promised to check in. 0 means
-- it never declared one (an older client, or a run only the indexer knows),
-- and such a run keeps the old 30-minute staleness window.
--
-- metric_goals says, per metric, whether lower ("min") or higher ("max") is
-- better. It lives on the project, not the run: "which run is best" is a
-- question about the whole comparison.
ALTER TABLE exp_runs
    ADD COLUMN IF NOT EXISTS summary_min JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE exp_runs
    ADD COLUMN IF NOT EXISTS summary_max JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE exp_runs
    ADD COLUMN IF NOT EXISTS heartbeat_secs INTEGER NOT NULL DEFAULT 0;
ALTER TABLE exp_projects
    ADD COLUMN IF NOT EXISTS metric_goals JSONB NOT NULL DEFAULT '{}'::jsonb;
