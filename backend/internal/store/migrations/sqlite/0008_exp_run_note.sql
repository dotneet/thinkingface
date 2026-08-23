-- Free-form note on a run (postgres/0014_exp_run_note.sql). Hand-maintained
-- like tags / archived / is_baseline, so UpsertExpRun never writes it and a
-- re-index cannot erase it.
ALTER TABLE exp_runs ADD COLUMN note TEXT NOT NULL DEFAULT '';
