-- thinkingface schema (SQLite port). Consolidates postgres/0001_init.sql through
-- postgres/0007_exp_run_one_baseline.sql into the single final state; there is
-- no per-migration history here because a SQLite DB always starts empty.
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
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    email         TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL,
    is_admin      INTEGER NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

CREATE TABLE IF NOT EXISTS namespaces (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT NOT NULL UNIQUE,
    kind          TEXT NOT NULL CHECK (kind IN ('user', 'org')),
    owner_user_id INTEGER REFERENCES users (id) ON DELETE CASCADE,
    created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

CREATE TABLE IF NOT EXISTS org_members (
    namespace_id INTEGER NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role         TEXT NOT NULL CHECK (role IN ('admin', 'write', 'read')),
    PRIMARY KEY (namespace_id, user_id)
);

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
    UNIQUE (namespace_id, name, kind)
);

CREATE INDEX IF NOT EXISTS idx_repositories_kind_updated ON repositories (kind, updated_at DESC);

-- Tag containment / license equality are the two facet filters the listing
-- pages apply most often. SQLite has no GIN index, so only the license
-- equality lookup gets an expression index; tag containment falls back to a
-- table scan (fine at this DB's scale).
CREATE INDEX IF NOT EXISTS idx_repositories_card_license ON repositories (card ->> 'license');

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

-- See the postgres migration for what storage_key means.
CREATE TABLE IF NOT EXISTS lfs_objects (
    oid         TEXT PRIMARY KEY,
    size        INTEGER NOT NULL,
    storage_key TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

CREATE INDEX IF NOT EXISTS idx_lfs_objects_storage_key ON lfs_objects (storage_key);

CREATE TABLE IF NOT EXISTS repo_lfs_objects (
    repo_id INTEGER NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    oid     TEXT NOT NULL REFERENCES lfs_objects (oid) ON DELETE CASCADE,
    PRIMARY KEY (repo_id, oid)
);

-- See the postgres migration for why this cannot live in sync_jobs.
CREATE TABLE IF NOT EXISTS storage_reclaim_jobs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT NOT NULL CHECK (kind IN ('repo_deleted')),
    payload    TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'pending'
               CHECK (status IN ('pending', 'running', 'done', 'failed')),
    attempts   INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

CREATE INDEX IF NOT EXISTS idx_storage_reclaim_pending ON storage_reclaim_jobs (status, id) WHERE status = 'pending';

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

-- tags / archived / is_baseline come from postgres/0002_run_annotations.sql;
-- folded into the initial CREATE TABLE here since SQLite has no
-- ADD COLUMN IF NOT EXISTS.
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
    UNIQUE (project_id, name)
);

-- The run list filters archived runs out by default, so the flag joins the
-- project key in the index the listing already walks.
CREATE INDEX IF NOT EXISTS idx_exp_runs_project_archived ON exp_runs (project_id, archived);

-- One baseline per experiment project (postgres/0007_exp_run_one_baseline.sql).
CREATE UNIQUE INDEX IF NOT EXISTS idx_exp_runs_one_baseline_per_project
    ON exp_runs (project_id)
    WHERE is_baseline = 1;

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

CREATE TABLE IF NOT EXISTS sync_jobs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id     INTEGER NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    ref         TEXT NOT NULL,
    old_sha     TEXT NOT NULL DEFAULT '',
    new_sha     TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'done', 'failed')),
    attempts    INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

CREATE INDEX IF NOT EXISTS idx_sync_jobs_pending ON sync_jobs (status, id) WHERE status = 'pending';

-- Lineage edges declared by a repository card (postgres/0003_repo_lineage.sql):
-- which datasets a model was trained on, which model it started from, and
-- which experiment run produced it. A dangling row (target does not exist,
-- or does not parse) is a normal state, not an error.
CREATE TABLE IF NOT EXISTS repo_lineage (
    repo_id          INTEGER NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    edge_kind        TEXT NOT NULL CHECK (edge_kind IN ('dataset', 'base_model', 'run')),
    raw              TEXT NOT NULL,
    target_namespace TEXT NOT NULL DEFAULT '',
    target_name      TEXT NOT NULL DEFAULT '',
    target_rev       TEXT NOT NULL DEFAULT '',
    target_project   TEXT NOT NULL DEFAULT '',
    target_run       TEXT NOT NULL DEFAULT '',
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

-- Webhooks: outbound event notifications for repository and experiment
-- activity, delivered through a queued worker (postgres/0005_webhooks.sql).
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
-- a repository page without scanning history (postgres/0006_download_stats.sql).
-- Cumulative downloads keep living on repositories.downloads.
CREATE TABLE IF NOT EXISTS repo_download_stats (
    repo_id INTEGER NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    date    DATE NOT NULL,
    count   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (repo_id, date)
);

CREATE INDEX IF NOT EXISTS idx_repo_download_stats_repo_date ON repo_download_stats (repo_id, date);

-- Full text search and facet filtering for repository listings
-- (postgres/0004_repo_search.sql). PostgreSQL maintains a trigger-fed
-- tsvector column; SQLite has no such type, so this is replaced by a plain
-- (non "external content") FTS5 virtual table whose rowid mirrors
-- repositories.id, kept in sync by triggers below.
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
