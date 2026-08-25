-- thinkingface schema (SQLite port). This is the whole schema in its final
-- state: the project consolidated its migration history into this one file
-- before its first release, so there is no per-migration history on either
-- dialect and a SQLite DB always starts empty.
--
-- Conversions from the PostgreSQL source of truth:
--   BIGSERIAL / BIGINT / INT      -> INTEGER (AUTOINCREMENT on primary keys)
--   BOOLEAN                       -> INTEGER (0/1)
--   TIMESTAMPTZ                   -> DATETIME, DEFAULT now() -> strftime(...) (UTC, ms)
--   JSONB                         -> TEXT (JSON stored as plain text)
--   TEXT[]                        -> TEXT (JSON array stored as plain text)
-- Foreign keys are enforced via the connection DSN (foreign_keys=ON), not PRAGMA
-- statements in this file. repositories.search_vector (PG trigger-maintained
-- tsvector) has no SQLite column counterpart; it is replaced by the
-- repositories_fts FTS5 virtual table + triggers at the end of this file.

CREATE TABLE IF NOT EXISTS users (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    username            TEXT NOT NULL UNIQUE,
    email               TEXT NOT NULL DEFAULT '',
    password_hash       TEXT NOT NULL,
    is_admin            INTEGER NOT NULL DEFAULT 0,
    created_at          DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    -- Session revocation handle: tf_session is a stateless signed value, so
    -- bumping this is what invalidates every cookie already handed out.
    session_epoch       INTEGER NOT NULL DEFAULT 0,
    -- Account suspension: the offboarding switch. NULL means active.
    disabled_at         DATETIME,
    disabled_by         INTEGER REFERENCES users (id) ON DELETE SET NULL,
    last_login_at       DATETIME,
    -- The approval column records the *pending* instant rather than an
    -- approved one, because NULL has to mean approved: an instance that
    -- turns approval on must not lock out the accounts that already exist.
    approval_pending_at DATETIME
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username_lower ON users (LOWER(username));

CREATE INDEX IF NOT EXISTS idx_users_approval_pending
    ON users (approval_pending_at)
    WHERE approval_pending_at IS NOT NULL;

-- An organisation must not depend on its founder, so an org namespace keeps
-- owner_user_id NULL and records the founder in created_by instead; the
-- founder's authority comes from an ordinary org_members row.
--
-- SQLite cannot attach a NOT NULL constraint to a column added with a
-- non-constant default, so updated_at (and org_members.created_at /
-- updated_at below) stayed nullable through the migration history and stays
-- nullable here to keep the shape identical. Readers COALESCE them onto
-- created_at, and every write sets them explicitly, so no NULL is ever
-- observed.
CREATE TABLE IF NOT EXISTS namespaces (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    name                TEXT NOT NULL UNIQUE,
    kind                TEXT NOT NULL CHECK (kind IN ('user', 'org')),
    owner_user_id       INTEGER REFERENCES users (id) ON DELETE CASCADE,
    created_at          DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    display_name        TEXT NOT NULL DEFAULT '',
    description         TEXT NOT NULL DEFAULT '',
    website             TEXT NOT NULL DEFAULT '',
    avatar_url          TEXT NOT NULL DEFAULT '',
    members_visibility  TEXT NOT NULL DEFAULT 'members'
                        CHECK (members_visibility IN ('members', 'public')),
    created_by          INTEGER REFERENCES users (id) ON DELETE SET NULL,
    updated_at          DATETIME,
    -- Storage quota: the ceiling on what one namespace may keep in GCS.
    -- NULL means "no quota of its own"; the instance default applies.
    storage_quota_bytes INTEGER
                        CHECK (storage_quota_bytes IS NULL OR storage_quota_bytes >= 0)
);

-- Namespace names must be unique regardless of case: "Alice" and "alice"
-- cannot both exist. Names are always ASCII (backend/internal/api/repos.go
-- nameRe), so a plain LOWER() expression index is exact.
--
-- The column-level UNIQUE(name) above is now implied by this index. It is
-- left in place because SQLite cannot drop a column-level UNIQUE constraint
-- without rebuilding the table, which is not worth it for an inert
-- constraint, and because the Postgres side keeps its counterpart too.
CREATE UNIQUE INDEX IF NOT EXISTS idx_namespaces_name_lower ON namespaces (LOWER(name));

CREATE TABLE IF NOT EXISTS org_members (
    namespace_id INTEGER NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role         TEXT NOT NULL CHECK (role IN ('admin', 'write', 'read')),
    added_by     INTEGER REFERENCES users (id) ON DELETE SET NULL,
    created_at   DATETIME,
    updated_at   DATETIME,
    PRIMARY KEY (namespace_id, user_id)
);

CREATE INDEX IF NOT EXISTS org_members_user_idx ON org_members (user_id);

CREATE TABLE IF NOT EXISTS org_audit_log (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    namespace_id   INTEGER NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    actor_user_id  INTEGER REFERENCES users (id) ON DELETE SET NULL,
    actor_name     TEXT NOT NULL,
    action         TEXT NOT NULL,
    target_user_id INTEGER REFERENCES users (id) ON DELETE SET NULL,
    target_name    TEXT NOT NULL DEFAULT '',
    details        TEXT NOT NULL DEFAULT '{}',
    created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

CREATE INDEX IF NOT EXISTS org_audit_log_ns_idx ON org_audit_log (namespace_id, id DESC);

CREATE TABLE IF NOT EXISTS access_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    scope        TEXT NOT NULL CHECK (scope IN ('read', 'write')),
    last_used_at DATETIME,
    expires_at   DATETIME,
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

-- SSH public keys, used to authenticate git over SSH
-- (docs/dev/thinkingface-design.md §5 "Git over SSH").
CREATE TABLE IF NOT EXISTS user_ssh_keys (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title        TEXT NOT NULL,
    public_key   TEXT NOT NULL,
    fingerprint  TEXT NOT NULL UNIQUE,
    last_used_at DATETIME,
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

CREATE INDEX IF NOT EXISTS idx_user_ssh_keys_user ON user_ssh_keys (user_id, created_at DESC);

-- storage_path decouples a repository's physical location from its logical
-- name, so a rename or a transfer never has to move bytes
-- (docs/dev/repo-transfer-design.md §3). New repositories get "repos/{ulid}".
CREATE TABLE IF NOT EXISTS repositories (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    namespace_id   INTEGER NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    kind           TEXT NOT NULL CHECK (kind IN ('dataset', 'model')),
    default_branch TEXT NOT NULL DEFAULT 'main',
    description    TEXT NOT NULL DEFAULT '',
    card           TEXT NOT NULL DEFAULT '{}',
    head_sha       TEXT NOT NULL DEFAULT '',
    total_size     INTEGER NOT NULL DEFAULT 0,
    downloads      INTEGER NOT NULL DEFAULT 0,
    is_experiment  INTEGER NOT NULL DEFAULT 0,
    created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    storage_path   TEXT NOT NULL DEFAULT '',
    -- Archive is soft and reversible: an archived repository stays fully
    -- readable and only refuses writes.
    archived_at    DATETIME,
    archived_by    INTEGER REFERENCES users (id) ON DELETE SET NULL,
    UNIQUE (namespace_id, name, kind)
);

CREATE INDEX IF NOT EXISTS idx_repositories_kind_updated ON repositories (kind, updated_at DESC);

-- Tag containment / license equality are the two facet filters the listing
-- pages apply most often. SQLite has no GIN index, so only the license
-- equality lookup gets an expression index; tag containment falls back to a
-- table scan (fine at this DB's scale).
CREATE INDEX IF NOT EXISTS idx_repositories_card_license ON repositories (card ->> 'license');

CREATE UNIQUE INDEX IF NOT EXISTS idx_repositories_storage_path ON repositories (storage_path);

CREATE INDEX IF NOT EXISTS idx_repositories_archived ON repositories (archived_at) WHERE archived_at IS NOT NULL;

-- Repository transfer (docs/dev/repo-transfer-design.md §4, §7): the old
-- location keeps answering through a redirect row, and a transfer that needs
-- the receiving side's consent waits here as a pending row.
CREATE TABLE IF NOT EXISTS repo_redirects (
    kind           TEXT    NOT NULL CHECK (kind IN ('dataset', 'model')),
    from_namespace TEXT    NOT NULL,
    from_name      TEXT    NOT NULL,
    repo_id        INTEGER NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    PRIMARY KEY (kind, from_namespace, from_name)
);

CREATE INDEX IF NOT EXISTS idx_repo_redirects_repo ON repo_redirects (repo_id);

-- Redirect lookups fold the namespace's case, like every other namespace
-- lookup, so the fold costs no scan.
CREATE INDEX IF NOT EXISTS idx_repo_redirects_from_lower
    ON repo_redirects (kind, LOWER(from_namespace), from_name);

CREATE TABLE IF NOT EXISTS repo_transfers (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id           INTEGER NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    from_namespace_id INTEGER NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    from_name         TEXT    NOT NULL,
    to_namespace_id   INTEGER NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    to_name           TEXT    NOT NULL,
    requested_by      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    status            TEXT    NOT NULL CHECK (status IN ('pending', 'accepted', 'rejected', 'cancelled', 'expired')),
    decided_by        INTEGER REFERENCES users (id) ON DELETE SET NULL,
    decided_at        DATETIME,
    expires_at        DATETIME NOT NULL,
    created_at        DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_repo_transfers_one_pending ON repo_transfers (repo_id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_repo_transfers_to_pending ON repo_transfers (to_namespace_id) WHERE status = 'pending';

-- Cached view of the default-branch tree so listings never have to touch git.
CREATE TABLE IF NOT EXISTS repo_files (
    repo_id    INTEGER NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    ref        TEXT NOT NULL,
    path       TEXT NOT NULL,
    size       INTEGER NOT NULL,
    blob_sha   TEXT NOT NULL,
    lfs_oid    TEXT,
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    PRIMARY KEY (repo_id, ref, path)
);

CREATE INDEX IF NOT EXISTS idx_repo_files_lookup ON repo_files (repo_id, ref, path);

-- LFS objects are content addressed: the object's key in the bucket is
-- derived from its oid, so it carries no namespace, no repository and no
-- name and never has to move (docs/dev/content-addressed-storage-design.md).
CREATE TABLE IF NOT EXISTS lfs_objects (
    oid        TEXT PRIMARY KEY,
    size       INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

CREATE TABLE IF NOT EXISTS repo_lfs_objects (
    repo_id INTEGER NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    oid     TEXT NOT NULL REFERENCES lfs_objects (oid) ON DELETE CASCADE,
    PRIMARY KEY (repo_id, oid)
);

CREATE TABLE IF NOT EXISTS parquet_files (
    repo_id        INTEGER NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    ref            TEXT NOT NULL,
    path           TEXT NOT NULL,
    num_rows       INTEGER NOT NULL,
    num_row_groups INTEGER NOT NULL,
    schema         TEXT NOT NULL DEFAULT '[]',
    indexed_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    PRIMARY KEY (repo_id, ref, path)
);

CREATE TABLE IF NOT EXISTS exp_projects (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id    INTEGER NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    UNIQUE (repo_id, name)
);

-- tags / archived / is_baseline / note are the hand-maintained metadata a
-- person attaches to a run: UpsertExpRun never writes them, so a re-index
-- cannot erase them. group_name / job_type are the opposite -- ingest
-- columns, as `init(group=..., job_type=...)` declared them -- but they are
-- written with the same "NULL means keep" rule, so a batch that omits them
-- leaves them alone. Empty string means "not in a group".
CREATE TABLE IF NOT EXISTS exp_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  INTEGER NOT NULL REFERENCES exp_projects (id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'finished',
    config      TEXT NOT NULL DEFAULT '{}',
    summary     TEXT NOT NULL DEFAULT '{}',
    metric_keys TEXT NOT NULL DEFAULT '[]',
    last_step   INTEGER NOT NULL DEFAULT 0,
    num_points  INTEGER NOT NULL DEFAULT 0,
    started_at  DATETIME,
    updated_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    tags        TEXT NOT NULL DEFAULT '[]',
    archived    INTEGER NOT NULL DEFAULT 0,
    is_baseline INTEGER NOT NULL DEFAULT 0,
    note        TEXT NOT NULL DEFAULT '',
    group_name  TEXT NOT NULL DEFAULT '',
    job_type    TEXT NOT NULL DEFAULT '',
    UNIQUE (project_id, name)
);

-- The run list filters archived runs out by default, so the flag joins the
-- project key in the index the listing already walks.
CREATE INDEX IF NOT EXISTS idx_exp_runs_project_archived ON exp_runs (project_id, archived);

-- One baseline per experiment project. UpdateExpRunAnnotation clears siblings
-- in the same transaction; this index is what makes that a hard guarantee.
CREATE UNIQUE INDEX IF NOT EXISTS idx_exp_runs_one_baseline_per_project
    ON exp_runs (project_id)
    WHERE is_baseline = 1;

-- Models a run declared it produced (`trackio.log_model("ns/name")`). The
-- run-side half of the lineage index: repo_lineage is replaced wholesale
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

-- Live metric points from the native ingest API. Batch-synced trackio data is
-- read from parquet on demand instead; this table only holds unflushed points.
CREATE TABLE IF NOT EXISTS exp_points (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id  INTEGER NOT NULL REFERENCES exp_runs (id) ON DELETE CASCADE,
    step    INTEGER NOT NULL,
    ts      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    metrics TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_exp_points_run_step ON exp_points (run_id, step);

-- The post-push work queue. lease_expires_at and next_attempt_at give the
-- queue ownership and retry pacing: SQLite mode runs a single writer, so the
-- two-replica race the lease closes cannot happen here, but the columns exist
-- in both dialects because the queries in store/jobs.go are shared -- the
-- retry backoff reads next_attempt_at on every backend, and a process killed
-- mid-sync leaves a stale 'running' row under SQLite exactly as it does under
-- Postgres. Both are nullable (NULL on next_attempt_at means "due now").
CREATE TABLE IF NOT EXISTS sync_jobs (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id          INTEGER NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    ref              TEXT NOT NULL,
    old_sha          TEXT NOT NULL DEFAULT '',
    new_sha          TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'done', 'failed')),
    attempts         INTEGER NOT NULL DEFAULT 0,
    last_error       TEXT NOT NULL DEFAULT '',
    created_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    kind             TEXT NOT NULL DEFAULT 'push',
    payload          TEXT NOT NULL DEFAULT '{}',
    lease_expires_at DATETIME,
    next_attempt_at  DATETIME
);

CREATE INDEX IF NOT EXISTS idx_sync_jobs_pending ON sync_jobs (status, id) WHERE status = 'pending';

-- ClaimSyncJob refuses a job whose repo+ref another worker is already
-- running, and this index is the lookup that decides it.
CREATE INDEX IF NOT EXISTS idx_sync_jobs_repo_ref_status ON sync_jobs (repo_id, ref, status);

CREATE INDEX IF NOT EXISTS idx_sync_jobs_due ON sync_jobs (status, next_attempt_at, id)
    WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_sync_jobs_lease ON sync_jobs (lease_expires_at)
    WHERE status = 'running';

-- Lineage edges declared by a repository card: which datasets a model was
-- trained on, which model it started from, which datasets it was evaluated
-- against, which repository supersedes it, and which experiment run produced
-- it (docs/dev/api-contract.md §12). A dangling row (target does not exist,
-- or does not parse) is a normal state, not an error. `relation` records how
-- a repository relates to the base model it points at; only 'base_model'
-- edges use it, and the rest keep ''.
CREATE TABLE IF NOT EXISTS repo_lineage (
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

-- Reverse lookup: "which repositories were built from this one?"
CREATE INDEX IF NOT EXISTS idx_repo_lineage_target
    ON repo_lineage (edge_kind, target_namespace, target_name);

-- Reverse lookup for a single experiment run.
CREATE INDEX IF NOT EXISTS idx_repo_lineage_run
    ON repo_lineage (target_namespace, target_name, target_project, target_run)
    WHERE edge_kind = 'run';

CREATE INDEX IF NOT EXISTS idx_repo_lineage_target_relation
    ON repo_lineage (target_namespace, target_name, relation)
    WHERE edge_kind = 'base_model';

CREATE INDEX IF NOT EXISTS idx_repo_lineage_new_version
    ON repo_lineage (target_namespace, target_name)
    WHERE edge_kind = 'new_version';

-- Webhooks: outbound event notifications for repository and experiment
-- activity, delivered through a queued worker.
CREATE TABLE IF NOT EXISTS webhooks (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    namespace_id INTEGER NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    -- NULL means "every repository in the namespace".
    repo_id      INTEGER REFERENCES repositories (id) ON DELETE CASCADE,
    url          TEXT NOT NULL,
    secret       TEXT NOT NULL,
    events       TEXT NOT NULL DEFAULT '[]',
    active       INTEGER NOT NULL DEFAULT 1,
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

CREATE INDEX IF NOT EXISTS idx_webhooks_namespace ON webhooks (namespace_id);
CREATE INDEX IF NOT EXISTS idx_webhooks_repo ON webhooks (repo_id) WHERE repo_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    webhook_id      INTEGER NOT NULL REFERENCES webhooks (id) ON DELETE CASCADE,
    event           TEXT NOT NULL,
    payload         TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'success', 'failed')),
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_attempt_at DATETIME,
    response_status INTEGER,
    -- Truncated to a few KB so a chatty endpoint cannot bloat this table.
    response_body   TEXT NOT NULL DEFAULT '',
    -- The queue's lease/backoff clock: a claim pushes this forward so a
    -- crashed worker's in-flight row becomes claimable again on its own
    -- (see internal/webhooks), and a failed attempt pushes it forward by the
    -- exponential backoff interval before the next retry.
    next_attempt_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_pending
    ON webhook_deliveries (next_attempt_at, id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_webhook
    ON webhook_deliveries (webhook_id, created_at DESC);

-- Daily download counters, used to answer "downloads in the last 30 days" on
-- a repository page without scanning history. Cumulative downloads keep
-- living on repositories.downloads; one count per resolve request advances
-- both (docs/dev/api-contract.md).
CREATE TABLE IF NOT EXISTS repo_download_stats (
    repo_id INTEGER NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    date    DATE NOT NULL,
    count   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (repo_id, date)
);

CREATE INDEX IF NOT EXISTS idx_repo_download_stats_repo_date ON repo_download_stats (repo_id, date);

-- Full text search and facet filtering for repository listings. PostgreSQL
-- maintains a trigger-fed tsvector column; SQLite has no such type, so this
-- is replaced by a plain (non "external content") FTS5 virtual table whose
-- rowid mirrors repositories.id, kept in sync by triggers below.
--
-- README bodies are never stored here (they live in git / GCS): the search
-- surface is the repository name plus the parsed README front matter
-- (repositories.card, see internal/repocard).
CREATE VIRTUAL TABLE IF NOT EXISTS repositories_fts USING fts5(
    name, tags, description, card_description, short_description, summary,
    license, pipeline_tag, task_categories,
    tokenize = 'unicode61'
);

CREATE TRIGGER IF NOT EXISTS repositories_fts_ai AFTER INSERT ON repositories BEGIN
    INSERT INTO repositories_fts
        (rowid, name, tags, description, card_description, short_description, summary, license, pipeline_tag, task_categories)
    VALUES (
        new.id,
        new.name,
        CASE json_type(new.card, '$.tags')
            WHEN 'array' THEN (SELECT coalesce(group_concat(value, ' '), '') FROM json_each(new.card, '$.tags'))
            WHEN 'text' THEN new.card ->> '$.tags'
            ELSE ''
        END,
        new.description,
        coalesce(new.card ->> '$.description', ''),
        coalesce(new.card ->> '$.short_description', ''),
        coalesce(new.card ->> '$.summary', ''),
        coalesce(new.card ->> '$.license', ''),
        CASE json_type(new.card, '$.pipeline_tag')
            WHEN 'array' THEN (SELECT coalesce(group_concat(value, ' '), '') FROM json_each(new.card, '$.pipeline_tag'))
            WHEN 'text' THEN new.card ->> '$.pipeline_tag'
            ELSE ''
        END,
        CASE json_type(new.card, '$.task_categories')
            WHEN 'array' THEN (SELECT coalesce(group_concat(value, ' '), '') FROM json_each(new.card, '$.task_categories'))
            WHEN 'text' THEN new.card ->> '$.task_categories'
            ELSE ''
        END
    );
END;

CREATE TRIGGER IF NOT EXISTS repositories_fts_au AFTER UPDATE ON repositories BEGIN
    DELETE FROM repositories_fts WHERE rowid = old.id;
    INSERT INTO repositories_fts
        (rowid, name, tags, description, card_description, short_description, summary, license, pipeline_tag, task_categories)
    VALUES (
        new.id,
        new.name,
        CASE json_type(new.card, '$.tags')
            WHEN 'array' THEN (SELECT coalesce(group_concat(value, ' '), '') FROM json_each(new.card, '$.tags'))
            WHEN 'text' THEN new.card ->> '$.tags'
            ELSE ''
        END,
        new.description,
        coalesce(new.card ->> '$.description', ''),
        coalesce(new.card ->> '$.short_description', ''),
        coalesce(new.card ->> '$.summary', ''),
        coalesce(new.card ->> '$.license', ''),
        CASE json_type(new.card, '$.pipeline_tag')
            WHEN 'array' THEN (SELECT coalesce(group_concat(value, ' '), '') FROM json_each(new.card, '$.pipeline_tag'))
            WHEN 'text' THEN new.card ->> '$.pipeline_tag'
            ELSE ''
        END,
        CASE json_type(new.card, '$.task_categories')
            WHEN 'array' THEN (SELECT coalesce(group_concat(value, ' '), '') FROM json_each(new.card, '$.task_categories'))
            WHEN 'text' THEN new.card ->> '$.task_categories'
            ELSE ''
        END
    );
END;

CREATE TRIGGER IF NOT EXISTS repositories_fts_ad AFTER DELETE ON repositories BEGIN
    DELETE FROM repositories_fts WHERE rowid = old.id;
END;
