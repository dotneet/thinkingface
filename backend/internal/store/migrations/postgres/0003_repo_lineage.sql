-- Lineage edges declared by a repository card (the `lineage:` block of
-- README.md's YAML front matter): which datasets a model was trained on, which
-- model it started from, and which experiment run produced it.
--
-- Every edge keeps both the normalised target and the verbatim string. A card
-- may name a repository that does not exist (yet), or one the reader is not
-- allowed to see; such an edge is still worth showing, so a dangling row is a
-- normal state rather than an error.

CREATE TABLE IF NOT EXISTS repo_lineage (
    repo_id          BIGINT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    edge_kind        TEXT NOT NULL CHECK (edge_kind IN ('dataset', 'base_model', 'run')),
    -- The reference exactly as the card spelled it, e.g. "team/imdb-ja@v1".
    raw              TEXT NOT NULL,
    -- Normalised target. Empty when the raw string does not parse as a
    -- reference at all, which keeps such an edge dangling forever by design.
    target_namespace TEXT NOT NULL DEFAULT '',
    target_name      TEXT NOT NULL DEFAULT '',
    target_rev       TEXT NOT NULL DEFAULT '',
    -- Only 'run' edges use these two.
    target_project   TEXT NOT NULL DEFAULT '',
    target_run       TEXT NOT NULL DEFAULT '',
    -- Position within its list in the card, so the UI can preserve the order
    -- the author wrote.
    ordinal          INT NOT NULL DEFAULT 0,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, edge_kind, raw)
);

-- Reverse lookup: "which repositories were built from this one?"
CREATE INDEX IF NOT EXISTS idx_repo_lineage_target
    ON repo_lineage (edge_kind, target_namespace, target_name);

-- Reverse lookup for a single experiment run.
CREATE INDEX IF NOT EXISTS idx_repo_lineage_run
    ON repo_lineage (target_namespace, target_name, target_project, target_run)
    WHERE edge_kind = 'run';
