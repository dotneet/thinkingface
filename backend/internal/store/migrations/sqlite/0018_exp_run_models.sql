-- Models a run declared it produced (postgres/0024_exp_run_models.sql).
-- The run-side half of the lineage index: repo_lineage is replaced wholesale
-- from the repository card on every push, so a run's own declaration lives
-- here instead. UpsertExpRun never writes it, so a re-index cannot erase it.
CREATE TABLE IF NOT EXISTS exp_run_models (
    run_id         INTEGER NOT NULL REFERENCES exp_runs (id) ON DELETE CASCADE,
    raw            TEXT NOT NULL,
    repo_namespace TEXT NOT NULL,
    repo_name      TEXT NOT NULL,
    revision       TEXT NOT NULL DEFAULT '',
    ordinal        INTEGER NOT NULL DEFAULT 0,
    updated_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    PRIMARY KEY (run_id, raw)
);

CREATE INDEX IF NOT EXISTS idx_exp_run_models_target
    ON exp_run_models (repo_namespace, repo_name);
