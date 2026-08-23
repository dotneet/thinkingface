-- Models a run declared it produced (`trackio.log_model("ns/name")`).
--
-- This is the run-side half of the lineage index: repo_lineage holds what a
-- repository *card* declares, and the sync worker replaces that set wholesale
-- on every default-branch push -- so a row written here by the ingest API
-- would be wiped by the next push to the model. Keeping the declaration next
-- to the run it came from leaves exactly one writer per store.
--
-- Like tags / archived / is_baseline / note it is a hand-maintained (well,
-- script-maintained) annotation: UpsertExpRun never touches it, so
-- re-indexing the project's parquet cannot erase it.
CREATE TABLE IF NOT EXISTS exp_run_models (
    run_id         BIGINT NOT NULL REFERENCES exp_runs (id) ON DELETE CASCADE,
    -- raw is the reference exactly as the script wrote it ("ns/name@rev"). It
    -- is part of the key so one run may record two revisions of the same
    -- repository (a checkpoint and its later re-upload) without collapsing.
    raw            TEXT NOT NULL,
    repo_namespace TEXT NOT NULL,
    repo_name      TEXT NOT NULL,
    revision       TEXT NOT NULL DEFAULT '',
    ordinal        INTEGER NOT NULL DEFAULT 0,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, raw)
);

-- The reverse lookup the model page runs: "which runs claim to have produced
-- this repository?".
CREATE INDEX IF NOT EXISTS idx_exp_run_models_target
    ON exp_run_models (repo_namespace, repo_name);
