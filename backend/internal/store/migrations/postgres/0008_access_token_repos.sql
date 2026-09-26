-- Access tokens restricted to a list of repositories
-- (docs/dev/agent-features.md §3). A token with at least one row here may
-- change only the repositories its rows name; a token with none is
-- unrestricted, exactly as every token was before this table existed.
--
-- Each row names its repository twice, and the two halves have different
-- jobs:
--
--   repo_id is what enforcement matches on. An id, not a name, so the grant
--   follows the repository it was made for through a rename or a transfer,
--   and -- the case that decides it -- never silently extends to a *different*
--   repository that later takes the name: delete alice/exp, create a new
--   alice/exp, and a token minted for the first one has no power over the
--   second. What a transfer does NOT do is carry any authority with it: the
--   owner's ordinary write permission is re-checked on every request as well,
--   so a repository that moves somewhere its token's owner cannot write is out
--   of reach for the token too.
--
--   repo_kind / namespace / name are the name the repository had when the
--   token was minted. They are display-only (the listing prefers the current
--   name when the repository still exists) and they are the primary key, which
--   is what keeps a row alive when its repository is deleted: repo_id goes
--   NULL rather than the row going away. That is the fail-closed half. Had the
--   row cascaded away with the repository, a token whose every listed
--   repository had been deleted would have *no* rows -- which reads as
--   "unrestricted", turning the narrowest token its owner had into the
--   broadest. A NULL repo_id matches nothing, so such a token can write
--   nowhere.
CREATE TABLE IF NOT EXISTS access_token_repos (
    token_id  BIGINT NOT NULL REFERENCES access_tokens (id) ON DELETE CASCADE,
    repo_id   BIGINT REFERENCES repositories (id) ON DELETE SET NULL,
    repo_kind TEXT   NOT NULL CHECK (repo_kind IN ('dataset', 'model')),
    namespace TEXT   NOT NULL,
    name      TEXT   NOT NULL,
    PRIMARY KEY (token_id, repo_kind, namespace, name)
);

-- ON DELETE SET NULL scans for the deleted repository's rows; without this
-- every repository delete would read the whole table.
CREATE INDEX IF NOT EXISTS idx_access_token_repos_repo ON access_token_repos (repo_id);
