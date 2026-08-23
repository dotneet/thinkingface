-- thinkingface initial schema

CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    email         TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL,
    is_admin      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS namespaces (
    id            BIGSERIAL PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    kind          TEXT NOT NULL CHECK (kind IN ('user', 'org')),
    owner_user_id BIGINT REFERENCES users (id) ON DELETE CASCADE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS org_members (
    namespace_id BIGINT NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    user_id      BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role         TEXT NOT NULL CHECK (role IN ('admin', 'write', 'read')),
    PRIMARY KEY (namespace_id, user_id)
);

CREATE TABLE IF NOT EXISTS access_tokens (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    token_hash  TEXT NOT NULL UNIQUE,
    scope       TEXT NOT NULL CHECK (scope IN ('read', 'write')),
    last_used_at TIMESTAMPTZ,
    expires_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS repositories (
    id             BIGSERIAL PRIMARY KEY,
    namespace_id   BIGINT NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    kind           TEXT NOT NULL CHECK (kind IN ('dataset', 'model')),
    default_branch TEXT NOT NULL DEFAULT 'main',
    description    TEXT NOT NULL DEFAULT '',
    card           JSONB NOT NULL DEFAULT '{}'::jsonb,
    head_sha       TEXT NOT NULL DEFAULT '',
    total_size     BIGINT NOT NULL DEFAULT 0,
    downloads      BIGINT NOT NULL DEFAULT 0,
    is_experiment  BOOLEAN NOT NULL DEFAULT FALSE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (namespace_id, name, kind)
);

CREATE INDEX IF NOT EXISTS idx_repositories_kind_updated ON repositories (kind, updated_at DESC);

-- Cached view of the default-branch tree so listings never have to touch git.
CREATE TABLE IF NOT EXISTS repo_files (
    repo_id    BIGINT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    ref        TEXT NOT NULL,
    path       TEXT NOT NULL,
    size       BIGINT NOT NULL,
    blob_sha   TEXT NOT NULL,
    lfs_oid    TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, ref, path)
);

CREATE INDEX IF NOT EXISTS idx_repo_files_lookup ON repo_files (repo_id, ref, path text_pattern_ops);

-- storage_key is where the bytes actually live: "lfs/{oid[0:2]}/{oid[2:4]}/{oid}"
-- while the object has no home under exports/, and the exports/ key itself once
-- the syncer moves it there (docs/single-copy-storage-design.md §5). Every read
-- path resolves through this column rather than recomputing storage.LFSKey, so
-- an object is stored once instead of twice.
CREATE TABLE IF NOT EXISTS lfs_objects (
    oid         TEXT PRIMARY KEY,
    size        BIGINT NOT NULL,
    storage_key TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The reverse lookup deleting an exports/ object needs: is this key some
-- object's only copy? (docs/single-copy-storage-design.md §7.2)
CREATE INDEX IF NOT EXISTS idx_lfs_objects_storage_key ON lfs_objects (storage_key);

CREATE TABLE IF NOT EXISTS repo_lfs_objects (
    repo_id BIGINT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    oid     TEXT NOT NULL REFERENCES lfs_objects (oid) ON DELETE CASCADE,
    PRIMARY KEY (repo_id, oid)
);

-- Cleanup that has to outlive the repository it belongs to. sync_jobs cannot
-- carry it: its repo_id is NOT NULL ON DELETE CASCADE, so a job queued for a
-- deleted repository disappears with it. Deleting a repository's exports/ tree
-- is exactly such a task, and it can no longer be done inline: an exports/
-- object may be an LFS blob's only copy, which has to be relocated before the
-- key goes away (docs/single-copy-storage-design.md §7.3).
--
-- Deliberately free of foreign keys: every row describes work on object
-- storage, identified by the payload alone.
CREATE TABLE IF NOT EXISTS storage_reclaim_jobs (
    id         BIGSERIAL PRIMARY KEY,
    kind       TEXT NOT NULL CHECK (kind IN ('repo_deleted')),
    -- {"kind":"dataset","namespace":"alice","name":"foo"}
    payload    JSONB NOT NULL,
    status     TEXT NOT NULL DEFAULT 'pending'
               CHECK (status IN ('pending', 'running', 'done', 'failed')),
    attempts   INT NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_storage_reclaim_pending ON storage_reclaim_jobs (status, id) WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS parquet_files (
    repo_id        BIGINT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    ref            TEXT NOT NULL,
    path           TEXT NOT NULL,
    num_rows       BIGINT NOT NULL,
    num_row_groups INT NOT NULL,
    schema         JSONB NOT NULL DEFAULT '[]'::jsonb,
    indexed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, ref, path)
);

CREATE TABLE IF NOT EXISTS exp_projects (
    id         BIGSERIAL PRIMARY KEY,
    repo_id    BIGINT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (repo_id, name)
);

CREATE TABLE IF NOT EXISTS exp_runs (
    id          BIGSERIAL PRIMARY KEY,
    project_id  BIGINT NOT NULL REFERENCES exp_projects (id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'finished',
    config      JSONB NOT NULL DEFAULT '{}'::jsonb,
    summary     JSONB NOT NULL DEFAULT '{}'::jsonb,
    metric_keys JSONB NOT NULL DEFAULT '[]'::jsonb,
    last_step   BIGINT NOT NULL DEFAULT 0,
    num_points  BIGINT NOT NULL DEFAULT 0,
    started_at  TIMESTAMPTZ,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

-- Live metric points from the native ingest API. Batch-synced trackio data is
-- read from parquet on demand instead; this table only holds unflushed points.
CREATE TABLE IF NOT EXISTS exp_points (
    id        BIGSERIAL PRIMARY KEY,
    run_id    BIGINT NOT NULL REFERENCES exp_runs (id) ON DELETE CASCADE,
    step      BIGINT NOT NULL,
    ts        TIMESTAMPTZ NOT NULL DEFAULT now(),
    metrics   JSONB NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_exp_points_run_step ON exp_points (run_id, step);

CREATE TABLE IF NOT EXISTS sync_jobs (
    id          BIGSERIAL PRIMARY KEY,
    repo_id     BIGINT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    ref         TEXT NOT NULL,
    old_sha     TEXT NOT NULL DEFAULT '',
    new_sha     TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'done', 'failed')),
    attempts    INT NOT NULL DEFAULT 0,
    last_error  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sync_jobs_pending ON sync_jobs (status, id) WHERE status = 'pending';
