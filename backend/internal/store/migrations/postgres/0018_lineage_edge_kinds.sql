-- Two more lineage edge kinds (docs/dev/api-contract.md §12):
--
--   eval_dataset -- a dataset the repository was *evaluated* on, read from a
--                   model card's `model-index:`. It targets a dataset
--                   repository, like the 'dataset' kind, but says something
--                   different: evaluated on, not trained from.
--   new_version  -- the repository that supersedes this one. Unlike every
--                   other kind it targets a repository of the *same* kind as
--                   its source, so a dataset may declare a successor too.
--
-- Only the CHECK constraint changes; the columns already hold what both kinds
-- need. The constraint name is the one PostgreSQL generated for the inline
-- CHECK in 0003_repo_lineage.sql.
ALTER TABLE repo_lineage DROP CONSTRAINT IF EXISTS repo_lineage_edge_kind_check;
ALTER TABLE repo_lineage ADD CONSTRAINT repo_lineage_edge_kind_check
    CHECK (edge_kind IN ('dataset', 'base_model', 'run', 'eval_dataset', 'new_version'));

-- Reverse lookup for "which repositories does this one supersede?". The chain
-- walk in the other direction goes through idx_repo_lineage_repo (the primary
-- key's leading column) instead.
CREATE INDEX IF NOT EXISTS idx_repo_lineage_new_version
    ON repo_lineage (target_namespace, target_name)
    WHERE edge_kind = 'new_version';
