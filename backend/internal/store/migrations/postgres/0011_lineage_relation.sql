-- How a repository relates to the base model it points at: HuggingFace Hub's
-- `base_model_relation` (docs/dev/api-contract.md §12). Written by the sync worker
-- from the card's own declaration, or inferred from the repository's contents
-- when the card is silent.
--
-- Only 'base_model' edges use it; dataset and run edges keep ''. The value is
-- not constrained to the four known relations on purpose: a card may declare
-- something else, and carrying that through verbatim (the UI files it under
-- "other") is the same forgiving treatment a reference that does not resolve
-- already gets.
ALTER TABLE repo_lineage ADD COLUMN IF NOT EXISTS relation TEXT NOT NULL DEFAULT '';

-- Reverse lookup with a relation filter: "the quantised versions of this
-- model". Extends idx_repo_lineage_target rather than replacing it, so the
-- unfiltered listing keeps the index it already had.
CREATE INDEX IF NOT EXISTS idx_repo_lineage_target_relation
    ON repo_lineage (target_namespace, target_name, relation)
    WHERE edge_kind = 'base_model';
