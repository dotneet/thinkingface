-- Two more lineage edge kinds, 'eval_dataset' and 'new_version'
-- (postgres/0018_lineage_edge_kinds.sql, docs/api-contract.md §12).
--
-- SQLite cannot alter a CHECK constraint, so the table is rebuilt: create the
-- replacement, copy every row, drop the original, rename. The column list is
-- 0001_init.sql's plus the `relation` column 0005_lineage_relation.sql added.
-- Dropping the table drops its indexes with it, so both are recreated below.
--
-- No other table references repo_lineage, so the rebuild is safe with
-- foreign_keys ON: the only foreign key involved is repo_lineage's own, and
-- every repositories row it points at is still there.

CREATE TABLE repo_lineage_new (
    repo_id          INTEGER NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    edge_kind        TEXT NOT NULL CHECK (edge_kind IN ('dataset', 'base_model', 'run', 'eval_dataset', 'new_version')),
    raw              TEXT NOT NULL,
    target_namespace TEXT NOT NULL DEFAULT '',
    target_name      TEXT NOT NULL DEFAULT '',
    target_rev       TEXT NOT NULL DEFAULT '',
    target_project   TEXT NOT NULL DEFAULT '',
    target_run       TEXT NOT NULL DEFAULT '',
    relation         TEXT NOT NULL DEFAULT '',
    ordinal          INTEGER NOT NULL DEFAULT 0,
    updated_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    PRIMARY KEY (repo_id, edge_kind, raw)
);

INSERT INTO repo_lineage_new (repo_id, edge_kind, raw, target_namespace, target_name,
                              target_rev, target_project, target_run, relation, ordinal, updated_at)
SELECT repo_id, edge_kind, raw, target_namespace, target_name,
       target_rev, target_project, target_run, relation, ordinal, updated_at
FROM repo_lineage;

DROP TABLE repo_lineage;

ALTER TABLE repo_lineage_new RENAME TO repo_lineage;

CREATE INDEX IF NOT EXISTS idx_repo_lineage_target
    ON repo_lineage (edge_kind, target_namespace, target_name);

CREATE INDEX IF NOT EXISTS idx_repo_lineage_run
    ON repo_lineage (target_namespace, target_name, target_project, target_run)
    WHERE edge_kind = 'run';

CREATE INDEX IF NOT EXISTS idx_repo_lineage_target_relation
    ON repo_lineage (target_namespace, target_name, relation)
    WHERE edge_kind = 'base_model';

CREATE INDEX IF NOT EXISTS idx_repo_lineage_new_version
    ON repo_lineage (target_namespace, target_name)
    WHERE edge_kind = 'new_version';
