-- Sweep grouping on a run: which sweep it belongs to (`group_name`) and what
-- role it played in it (`job_type`), mirroring wandb/trackio's
-- `init(group=..., job_type=...)`.
--
-- Unlike tags / archived / is_baseline / note these are *ingest* fields: the
-- training script states them, not a person, so UpsertExpRun writes them --
-- but with the same "NULL means keep" rule every other ingest column follows,
-- so a batch that omits them (or a re-index of the project's parquet, which
-- knows nothing about them) cannot erase what the run declared at init().
--
-- Empty string means "not in a group", which is what every run recorded
-- before this column existed is, and what keeps the flat listing working
-- unchanged.
ALTER TABLE exp_runs ADD COLUMN IF NOT EXISTS group_name TEXT NOT NULL DEFAULT '';
ALTER TABLE exp_runs ADD COLUMN IF NOT EXISTS job_type TEXT NOT NULL DEFAULT '';
