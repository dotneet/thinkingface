-- How a repository relates to the base model it points at
-- (postgres/0011_lineage_relation.sql, docs/api-contract.md §12). Only
-- 'base_model' edges use it; dataset and run edges keep ''.
--
-- SQLite has no ADD COLUMN IF NOT EXISTS, but a migration runs exactly once
-- (schema_migrations records it by file name), so a plain ADD COLUMN is safe.
ALTER TABLE repo_lineage ADD COLUMN relation TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_repo_lineage_target_relation
    ON repo_lineage (target_namespace, target_name, relation)
    WHERE edge_kind = 'base_model';
