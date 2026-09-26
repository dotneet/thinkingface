-- Access tokens restricted to a list of repositories
-- (docs/dev/agent-features.md §3). The Postgres copy of this file carries the
-- full reasoning. In short: enforcement matches repo_id, so a grant follows
-- its repository through a rename or transfer but never extends to a new
-- repository that takes an old name; the owner's ordinary write permission is
-- re-checked on every request on top of it; and the name columns are the
-- primary key, so deleting a repository nulls repo_id rather than dropping the
-- row -- a restricted token whose repositories are all gone must stay
-- restricted (to nothing), never fall back to "no rows = unrestricted".
--
-- foreign_keys(ON) is set on every connection (sqlite.go), so both ON DELETE
-- actions below are enforced on this engine too.
CREATE TABLE IF NOT EXISTS access_token_repos (
    token_id  INTEGER NOT NULL REFERENCES access_tokens (id) ON DELETE CASCADE,
    repo_id   INTEGER REFERENCES repositories (id) ON DELETE SET NULL,
    repo_kind TEXT    NOT NULL CHECK (repo_kind IN ('dataset', 'model')),
    namespace TEXT    NOT NULL,
    name      TEXT    NOT NULL,
    PRIMARY KEY (token_id, repo_kind, namespace, name)
);

CREATE INDEX IF NOT EXISTS idx_access_token_repos_repo ON access_token_repos (repo_id);
