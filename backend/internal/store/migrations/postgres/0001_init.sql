-- thinkingface schema. This is the whole schema in its final state: the
-- project consolidated its migration history into this one file before its
-- first release, so there is no per-migration history on either dialect.
-- The SQLite port is migrations/sqlite/0001_init.sql; this file is the source
-- of truth and that one follows it.

CREATE TABLE IF NOT EXISTS users (
    id                  BIGSERIAL PRIMARY KEY,
    username            TEXT NOT NULL UNIQUE,
    email               TEXT NOT NULL DEFAULT '',
    password_hash       TEXT NOT NULL,
    is_admin            BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Session revocation handle. tf_session is a stateless signed value, so
    -- without a server-side counter an issued cookie stays valid for its whole
    -- TTL no matter what the user does. Logout and password changes increment
    -- this, and a cookie whose epoch no longer matches is rejected.
    session_epoch       BIGINT NOT NULL DEFAULT 0,
    -- Account suspension: the offboarding switch. Resetting a password
    -- deliberately does not revoke access tokens (api-contract.md §1.3) and
    -- never touched SSH keys either, so without this an administrator had no
    -- way to actually cut somebody off. disabled_at is checked by every
    -- identity path -- session cookie, password, bearer token and SSH public
    -- key -- so setting it stops all of them at once without deleting
    -- anything. disabled_by records who did it, for the same reason every
    -- other administrative action names its actor.
    disabled_at         TIMESTAMPTZ,
    disabled_by         BIGINT REFERENCES users (id) ON DELETE SET NULL,
    -- Moved only when a password mints a session (handleLogin). Access tokens
    -- and SSH keys carry their own last-used timestamps and deliberately do
    -- not touch this one: the question it answers is "is anybody still using
    -- this account", and an automation's nightly token is the wrong signal
    -- for that. NULL means the account has never signed in.
    last_login_at       TIMESTAMPTZ,
    -- When a self-registration was put in the waiting room
    -- (TF_SIGNUP_REQUIRE_APPROVAL), NULL once a site administrator has
    -- admitted it. It is deliberately the *pending* instant rather than an
    -- approved_at, so that NULL -- the default every INSERT that says nothing
    -- gets -- means approved. The reverse spelling would lock out every
    -- account created by an administrator.
    --
    -- A pending account authenticates on no path at all, exactly like
    -- disabled_at: both predicates live at the single exit of credential
    -- resolution and in the two statements that resolve a credential outside
    -- it (LookupToken, LookupSSHKey).
    approval_pending_at TIMESTAMPTZ
);

-- users.username carries its own exact-match UNIQUE above, and CreateUser
-- writes it alongside the namespace row in one transaction, so
-- idx_namespaces_name_lower already keeps two users differing only by case
-- from existing. Indexing the folded username as well makes that guarantee
-- local to the users table, so the case-insensitive login lookup
-- (store.GetUserByUsername) can never match more than one row even if a user
-- row is ever created without its namespace.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username_lower ON users (LOWER(username));

-- The account directory's default sort is by username, but the waiting room
-- is read as "who is pending", which is a tiny fraction of the table on any
-- instance where it is used at all.
CREATE INDEX IF NOT EXISTS idx_users_approval_pending
    ON users (approval_pending_at)
    WHERE approval_pending_at IS NOT NULL;

-- Organisations (docs/dev/organization-design.md §6.1): the profile columns
-- and policies live on the namespace row rather than in a table of their own,
-- which keeps the namespace table a single name space. They exist for user
-- namespaces too but are never used there (NULL / default).
--
-- An organisation must not depend on its founder, so owner_user_id stays NULL
-- for an org and created_by records the founder instead. That way (a)
-- removing the founder from org_members really removes their power, and (b)
-- deleting the founder's account does not cascade the whole organisation
-- away; their authority comes from an ordinary org_members row.
CREATE TABLE IF NOT EXISTS namespaces (
    id                  BIGSERIAL PRIMARY KEY,
    name                TEXT NOT NULL UNIQUE,
    kind                TEXT NOT NULL CHECK (kind IN ('user', 'org')),
    owner_user_id       BIGINT REFERENCES users (id) ON DELETE CASCADE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    display_name        TEXT NOT NULL DEFAULT '',
    description         TEXT NOT NULL DEFAULT '',
    website             TEXT NOT NULL DEFAULT '',
    avatar_url          TEXT NOT NULL DEFAULT '',
    members_visibility  TEXT NOT NULL DEFAULT 'members'
                        CHECK (members_visibility IN ('members', 'public')),
    created_by          BIGINT REFERENCES users (id) ON DELETE SET NULL,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Storage quotas: the ceiling on what one namespace may keep in GCS.
    -- Object storage is billed by the byte and nothing on the upload path
    -- looked at how much a namespace already held, so a single account could
    -- fill the bucket -- and the bill -- without limit.
    --
    -- The quota is per namespace rather than per user or per repository: a
    -- namespace is what both an account and an organisation are, it is what
    -- the usage aggregation already groups by, and it is the unit an operator
    -- thinks in when deciding who gets how much.
    --
    -- NULL means "no quota of its own", not "unlimited": the instance-wide
    -- default (TF_DEFAULT_STORAGE_QUOTA_BYTES) applies to those rows, and it
    -- is unlimited only when that is unset. A stored 0 is a real quota of
    -- zero bytes -- an account that may create repositories but upload
    -- nothing -- so the two are deliberately distinguishable, here and all
    -- the way out to PATCH /api/v1/admin/namespaces/{ns}.
    --
    -- Only a site administrator may write this column. An organisation admin
    -- able to raise their own cap would not be a cap, so it is deliberately
    -- absent from the organisation settings API.
    storage_quota_bytes BIGINT
                        CHECK (storage_quota_bytes IS NULL OR storage_quota_bytes >= 0)
);

-- Namespace names must be unique regardless of case: "Alice" and "alice" are
-- the same identifier everywhere it is used downstream (the /{ns}/{name}
-- route, the HF-compatible /datasets/{ns}/{name} shape, git remotes, LFS key
-- layout). namespaces.name is always ASCII -- backend/internal/api/repos.go's
-- nameRe restricts it to [A-Za-z0-9._-] -- so a plain SQL LOWER() fold is
-- exact and needs no locale/ICU support.
--
-- The UNIQUE(name) constraint above is now implied by this index (two rows
-- with the same exact name also share the same lowercased name), so it is
-- inert rather than wrong, and is kept rather than dropped because dropping a
-- plain-column UNIQUE constraint is more churn than the redundancy is worth.
CREATE UNIQUE INDEX IF NOT EXISTS idx_namespaces_name_lower ON namespaces (LOWER(name));

CREATE TABLE IF NOT EXISTS org_members (
    namespace_id BIGINT NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    user_id      BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role         TEXT NOT NULL CHECK (role IN ('admin', 'write', 'read')),
    added_by     BIGINT REFERENCES users (id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (namespace_id, user_id)
);

CREATE INDEX IF NOT EXISTS org_members_user_idx ON org_members (user_id);

CREATE TABLE IF NOT EXISTS org_audit_log (
    id             BIGSERIAL PRIMARY KEY,
    namespace_id   BIGINT NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    actor_user_id  BIGINT REFERENCES users (id) ON DELETE SET NULL,
    -- Denormalised so the line still reads after the account is deleted.
    actor_name     TEXT NOT NULL,
    action         TEXT NOT NULL,
    target_user_id BIGINT REFERENCES users (id) ON DELETE SET NULL,
    -- Target username, repository full name, or webhook URL, per action.
    target_name    TEXT NOT NULL DEFAULT '',
    details        JSONB NOT NULL DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS org_audit_log_ns_idx ON org_audit_log (namespace_id, id DESC);

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

-- SSH public keys, used to authenticate git over SSH
-- (docs/dev/thinkingface-design.md §5 "Git over SSH").
--
-- fingerprint is the OpenSSH "SHA256:<base64>" form and is globally unique,
-- not unique per user: the SSH server has only the offered key to go on when
-- it resolves an identity, so the same key registered by two accounts would
-- make that resolution ambiguous.
CREATE TABLE IF NOT EXISTS user_ssh_keys (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title        TEXT NOT NULL,
    public_key   TEXT NOT NULL,
    fingerprint  TEXT NOT NULL UNIQUE,
    last_used_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_user_ssh_keys_user ON user_ssh_keys (user_id, created_at DESC);

-- repo_card_text extracts a card field as searchable text regardless of
-- whether the author wrote it as a single string (`license: mit`) or a list
-- (`task_categories: [text-classification, summarization]`).
CREATE OR REPLACE FUNCTION repo_card_text(card JSONB, key TEXT) RETURNS TEXT AS $$
    SELECT CASE jsonb_typeof(card -> key)
        WHEN 'array' THEN (
            SELECT COALESCE(string_agg(v, ' '), '') FROM jsonb_array_elements_text(card -> key) AS v
        )
        WHEN 'string' THEN card ->> key
        ELSE ''
    END;
$$ LANGUAGE sql IMMUTABLE;

-- search_vector is the full text search surface for repository listings.
-- README bodies are never stored in Postgres (they live in git / GCS), so it
-- is limited to what is already indexed: the repository name plus the parsed
-- README front matter (`repositories.card`, see internal/repocard). It is
-- trigger-maintained rather than a generated column because flattening
-- `card->'tags'` (a jsonb array) into lexemes needs
-- jsonb_array_elements_text(), and Postgres generated column expressions may
-- not contain subqueries.
--
-- storage_path decouples a repository's physical location from its logical
-- name so that transferring or renaming one never moves data
-- (docs/dev/repo-transfer-design.md §3). It is assigned once, at creation,
-- and never changes; the WAL prefix (wal/{storage_path}/) and the local bare
-- directory ({root}/{storage_path}.git) are derived from it. New repositories
-- get an opaque "repos/{ulid}".
--
-- archived_at doubles as the archive flag (NULL = active) and the audit
-- timestamp: an archived repository stays fully readable and downloadable but
-- rejects every write -- git push, HF commit, in-browser edit, transfer,
-- experiment ingest. Deleting it is still allowed, which is why this is a
-- nullable timestamp on the row rather than a move to a tombstone table.
-- archived_by survives that user's deletion as NULL rather than dragging the
-- repository down with it.
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
    search_vector  TSVECTOR NOT NULL DEFAULT ''::tsvector,
    storage_path   TEXT NOT NULL DEFAULT '',
    archived_at    TIMESTAMPTZ,
    archived_by    BIGINT REFERENCES users (id) ON DELETE SET NULL,
    UNIQUE (namespace_id, name, kind)
);

CREATE OR REPLACE FUNCTION repositories_search_vector_trigger() RETURNS TRIGGER AS $$
BEGIN
    NEW.search_vector :=
        setweight(to_tsvector('simple', coalesce(NEW.name, '')), 'A') ||
        setweight(to_tsvector('simple', repo_card_text(NEW.card, 'tags')), 'A') ||
        setweight(to_tsvector('simple', coalesce(NEW.description, '')), 'B') ||
        setweight(to_tsvector('simple', coalesce(NEW.card ->> 'description', '')), 'B') ||
        setweight(to_tsvector('simple', coalesce(NEW.card ->> 'short_description', '')), 'B') ||
        setweight(to_tsvector('simple', coalesce(NEW.card ->> 'summary', '')), 'B') ||
        setweight(to_tsvector('simple', coalesce(NEW.card ->> 'license', '')), 'C') ||
        setweight(to_tsvector('simple', repo_card_text(NEW.card, 'pipeline_tag')), 'C') ||
        setweight(to_tsvector('simple', repo_card_text(NEW.card, 'task_categories')), 'C');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_repositories_search_vector ON repositories;
CREATE TRIGGER trg_repositories_search_vector
    BEFORE INSERT OR UPDATE ON repositories
    FOR EACH ROW EXECUTE FUNCTION repositories_search_vector_trigger();

CREATE INDEX IF NOT EXISTS idx_repositories_kind_updated ON repositories (kind, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_repositories_search_vector ON repositories USING GIN (search_vector);

-- Tag containment (`card->'tags' @> '["a","b"]'`) and license equality are
-- the two facet filters the listing pages apply most often; index both.
CREATE INDEX IF NOT EXISTS idx_repositories_card_tags ON repositories USING GIN ((card -> 'tags'));
CREATE INDEX IF NOT EXISTS idx_repositories_card_license ON repositories ((card ->> 'license'));

CREATE UNIQUE INDEX IF NOT EXISTS idx_repositories_storage_path ON repositories (storage_path);

-- Partial: the listing filters `archived=false` far more often than it asks
-- for the archived ones, and active rows are the overwhelming majority.
CREATE INDEX IF NOT EXISTS idx_repositories_archived ON repositories (archived_at) WHERE archived_at IS NOT NULL;

-- Repository transfer (docs/dev/repo-transfer-design.md §4, §7): moving a
-- repository to another namespace (and optionally renaming it) never moves
-- git/LFS/WAL data, only repositories.namespace_id/name plus the bookkeeping
-- below.

-- Every former (kind, namespace, name) a repository used to be reachable at,
-- so a request for the old name resolves to the current row instead of
-- 404ing. A name is claimed by at most one row: a transfer upserts it, and
-- creating a new repository at a redirected-from name deletes it (the new
-- repository wins -- docs/dev/repo-transfer-design.md §5 "conflicts").
CREATE TABLE IF NOT EXISTS repo_redirects (
    kind           TEXT   NOT NULL CHECK (kind IN ('dataset', 'model')),
    from_namespace TEXT   NOT NULL,
    from_name      TEXT   NOT NULL,
    repo_id        BIGINT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, from_namespace, from_name)
);

CREATE INDEX IF NOT EXISTS idx_repo_redirects_repo ON repo_redirects (repo_id);

-- Redirect lookups fold the namespace's case, like every other namespace
-- lookup in this schema: /Alice/foo and /alice/foo are one repository before
-- a transfer, so they must stay one repository after it. from_namespace is
-- always a canonical namespace name and therefore ASCII, so a plain LOWER()
-- fold is exact.
--
-- The PRIMARY KEY above cannot serve the folded predicate, so index it
-- explicitly: the lookup runs on the 404 path of every repository read, which
-- is exactly where a sequential scan must not appear. from_name stays exact
-- -- repository names are case-sensitive everywhere (see GetRepo).
CREATE INDEX IF NOT EXISTS idx_repo_redirects_from_lower
    ON repo_redirects (kind, LOWER(from_namespace), from_name);

-- Transfer requests/records. An immediate move (actor has write access to
-- both sides) still leaves one 'accepted' row for audit; a move to a
-- namespace the actor cannot write to starts 'pending' and needs the target
-- namespace's owner/admin to accept it (docs/dev/repo-transfer-design.md §7.2).
CREATE TABLE IF NOT EXISTS repo_transfers (
    id                BIGSERIAL PRIMARY KEY,
    repo_id           BIGINT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    from_namespace_id BIGINT NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    -- The repository's name at request time, kept so the row still makes
    -- sense after the repository has since moved again.
    from_name         TEXT   NOT NULL,
    to_namespace_id   BIGINT NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    to_name           TEXT   NOT NULL,          -- transfer may rename at the same time; usually equal to from_name
    requested_by      BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    status            TEXT   NOT NULL CHECK (status IN ('pending', 'accepted', 'rejected', 'cancelled', 'expired')),
    decided_by        BIGINT REFERENCES users (id) ON DELETE SET NULL,
    decided_at        TIMESTAMPTZ,
    expires_at        TIMESTAMPTZ NOT NULL,     -- pending validity window (7 days, set by the caller)
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- At most one pending transfer per repository at a time.
CREATE UNIQUE INDEX IF NOT EXISTS idx_repo_transfers_one_pending ON repo_transfers (repo_id) WHERE status = 'pending';
-- Fast lookup of "what is pending that I could accept" by target namespace.
CREATE INDEX IF NOT EXISTS idx_repo_transfers_to_pending ON repo_transfers (to_namespace_id) WHERE status = 'pending';

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

-- LFS objects are content addressed: the bytes live at
-- lfs/{oid[0:2]}/{oid[2:4]}/{oid}, derived from the oid alone, so the key
-- carries no namespace, no repository and no ref and never has to move
-- (docs/dev/content-addressed-storage-design.md). Transferring, renaming or
-- deleting a repository moves nothing in the bucket; `thinkingface gc`
-- reclaims unreferenced objects instead.
CREATE TABLE IF NOT EXISTS lfs_objects (
    oid        TEXT PRIMARY KEY,
    size       BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS repo_lfs_objects (
    repo_id BIGINT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    oid     TEXT NOT NULL REFERENCES lfs_objects (oid) ON DELETE CASCADE,
    PRIMARY KEY (repo_id, oid)
);

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

-- tags / archived / is_baseline / note are run annotations: the
-- hand-maintained metadata a person attaches to a run while comparing
-- experiments. None of it comes from the ingest path or the parquet export,
-- so the columns must survive re-indexing -- UpsertExpRun never lists them,
-- which leaves the stored values alone on conflict.
--
-- group_name / job_type are the opposite: *ingest* fields mirroring
-- wandb/trackio's `init(group=..., job_type=...)`, stated by the training
-- script rather than by a person, so UpsertExpRun does write them -- but with
-- the same "NULL means keep" rule every other ingest column follows, so a
-- batch that omits them (or a re-index of the project's parquet, which knows
-- nothing about them) cannot erase what the run declared at init(). Empty
-- string means "not in a group".
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
    tags        TEXT[] NOT NULL DEFAULT '{}',
    archived    BOOLEAN NOT NULL DEFAULT FALSE,
    is_baseline BOOLEAN NOT NULL DEFAULT FALSE,
    note        TEXT NOT NULL DEFAULT '',
    group_name  TEXT NOT NULL DEFAULT '',
    job_type    TEXT NOT NULL DEFAULT '',
    UNIQUE (project_id, name)
);

-- The run list filters archived runs out by default, so the flag joins the
-- project key in the index the listing already walks.
CREATE INDEX IF NOT EXISTS idx_exp_runs_project_archived ON exp_runs (project_id, archived);

-- One baseline per experiment project. UpdateExpRunAnnotation clears siblings
-- then sets is_baseline inside a transaction, but two concurrent PATCHes that
-- both mark a previously-unset run can still both commit under READ COMMITTED:
-- the "clear others" updates lock no rows, and the two target rows never wait
-- on each other. The partial unique index is the actual invariant.
CREATE UNIQUE INDEX IF NOT EXISTS idx_exp_runs_one_baseline_per_project
    ON exp_runs (project_id)
    WHERE is_baseline;

-- Models a run declared it produced (`trackio.log_model("ns/name")`).
--
-- This is the run-side half of the lineage index: repo_lineage holds what a
-- repository *card* declares, and the sync worker replaces that set wholesale
-- on every default-branch push -- so a row written here by the ingest API
-- would be wiped by the next push to the model. Keeping the declaration next
-- to the run it came from leaves exactly one writer per store.
--
-- Like tags / archived / is_baseline / note it is a hand-maintained (well,
-- script-maintained) annotation: UpsertExpRun never touches it, so
-- re-indexing the project's parquet cannot erase it.
CREATE TABLE IF NOT EXISTS exp_run_models (
    run_id         BIGINT NOT NULL REFERENCES exp_runs (id) ON DELETE CASCADE,
    -- raw is the reference exactly as the script wrote it ("ns/name@rev"). It
    -- is part of the key so one run may record two revisions of the same
    -- repository (a checkpoint and its later re-upload) without collapsing.
    raw            TEXT NOT NULL,
    repo_namespace TEXT NOT NULL,
    repo_name      TEXT NOT NULL,
    revision       TEXT NOT NULL DEFAULT '',
    ordinal        INTEGER NOT NULL DEFAULT 0,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, raw)
);

-- The reverse lookup the model page runs: "which runs claim to have produced
-- this repository?".
CREATE INDEX IF NOT EXISTS idx_exp_run_models_target
    ON exp_run_models (repo_namespace, repo_name);

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

-- The post-push work queue. `kind` is 'push' (the default) plus whatever
-- kind-specific parameters `payload` carries.
--
-- lease_expires_at makes a claim explicit. Before it, a worker claimed a job
-- by flipping it to 'running' and nothing recorded whether that claim was
-- still alive; RequeueRunningJobs then reset every 'running' row at startup,
-- so a second replica booting while the first was mid-sync stole its job.
-- Both then walked their own OldSHA..NewSHA diff and published two disjoint
-- sets of blobs, leaving a file touched only by the job that finished first
-- with no blobs/{sha} object and nothing to republish it (see the comment on
-- ClaimSyncJob). infra defaults api_max_instances to 4, so this was reachable
-- on any ordinary scale-up, not just a redeploy. A worker now holds a job
-- only until its lease runs out and extends it while it is genuinely working,
-- so the sweeper can tell a crashed claim from a live one.
--
-- next_attempt_at paces retries. A failing job used to go straight back to
-- 'pending' and be reclaimed on the next tick, so all three attempts burned
-- within seconds and the row parked as 'failed' long before a transient GCS
-- 5xx had any chance to clear. It is nullable, with NULL meaning "due now",
-- rather than DEFAULT now(): SQLite rejects a non-constant default in ALTER
-- TABLE ADD COLUMN and rewrites now() to strftime(...) there, so keeping both
-- dialects nullable lets store/jobs.go run one shared query.
CREATE TABLE IF NOT EXISTS sync_jobs (
    id               BIGSERIAL PRIMARY KEY,
    repo_id          BIGINT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    ref              TEXT NOT NULL,
    old_sha          TEXT NOT NULL DEFAULT '',
    new_sha          TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'done', 'failed')),
    attempts         INT NOT NULL DEFAULT 0,
    last_error       TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    kind             TEXT  NOT NULL DEFAULT 'push',
    payload          JSONB NOT NULL DEFAULT '{}',
    lease_expires_at TIMESTAMPTZ,
    next_attempt_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_sync_jobs_pending ON sync_jobs (status, id) WHERE status = 'pending';

-- ClaimSyncJob refuses a job whose repo+ref another worker is already syncing,
-- which it decides with a NOT EXISTS over this table. idx_sync_jobs_pending
-- covers picking the next pending row; this one covers the sibling lookup, so
-- the added clause does not turn every claim into a sequential scan.
CREATE INDEX IF NOT EXISTS idx_sync_jobs_repo_ref_status ON sync_jobs (repo_id, ref, status);

-- The claim also filters on next_attempt_at and the sweeper scans running
-- rows by lease, so each gets an index rather than turning into a queue scan.
CREATE INDEX IF NOT EXISTS idx_sync_jobs_due ON sync_jobs (status, next_attempt_at, id)
    WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_sync_jobs_lease ON sync_jobs (lease_expires_at)
    WHERE status = 'running';

-- Lineage edges declared by a repository card (the `lineage:` block of
-- README.md's YAML front matter). The edge kinds (docs/dev/api-contract.md §12):
--
--   dataset      -- a dataset the repository was trained on
--   base_model   -- the model it started from
--   run          -- the experiment run that produced it
--   eval_dataset -- a dataset it was *evaluated* on, read from a model card's
--                   `model-index:`. It targets a dataset repository like
--                   'dataset' does, but says something different.
--   new_version  -- the repository that supersedes this one. Unlike every
--                   other kind it targets a repository of the *same* kind as
--                   its source, so a dataset may declare a successor too.
--
-- Every edge keeps both the normalised target and the verbatim string. A card
-- may name a repository that does not exist (yet), or one the reader is not
-- allowed to see; such an edge is still worth showing, so a dangling row is a
-- normal state rather than an error.
CREATE TABLE IF NOT EXISTS repo_lineage (
    repo_id          BIGINT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    edge_kind        TEXT NOT NULL CHECK (edge_kind IN ('dataset', 'base_model', 'run', 'eval_dataset', 'new_version')),
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
    -- How a repository relates to the base model it points at: HuggingFace
    -- Hub's `base_model_relation`. Written by the sync worker from the card's
    -- own declaration, or inferred from the repository's contents when the
    -- card is silent. Only 'base_model' edges use it; the rest keep ''. The
    -- value is not constrained to the four known relations on purpose: a card
    -- may declare something else, and carrying that through verbatim (the UI
    -- files it under "other") is the same forgiving treatment a reference
    -- that does not resolve already gets.
    relation         TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo_id, edge_kind, raw)
);

-- Reverse lookup: "which repositories were built from this one?"
CREATE INDEX IF NOT EXISTS idx_repo_lineage_target
    ON repo_lineage (edge_kind, target_namespace, target_name);

-- Reverse lookup for a single experiment run.
CREATE INDEX IF NOT EXISTS idx_repo_lineage_run
    ON repo_lineage (target_namespace, target_name, target_project, target_run)
    WHERE edge_kind = 'run';

-- Reverse lookup with a relation filter: "the quantised versions of this
-- model". Extends idx_repo_lineage_target rather than replacing it, so the
-- unfiltered listing keeps the index it already had.
CREATE INDEX IF NOT EXISTS idx_repo_lineage_target_relation
    ON repo_lineage (target_namespace, target_name, relation)
    WHERE edge_kind = 'base_model';

-- Reverse lookup for "which repositories does this one supersede?". The chain
-- walk in the other direction goes through the primary key's leading column
-- instead.
CREATE INDEX IF NOT EXISTS idx_repo_lineage_new_version
    ON repo_lineage (target_namespace, target_name)
    WHERE edge_kind = 'new_version';

-- Webhooks: outbound event notifications for repository and experiment
-- activity, delivered through a PG-queued worker (see webhook_deliveries).
CREATE TABLE IF NOT EXISTS webhooks (
    id           BIGSERIAL PRIMARY KEY,
    namespace_id BIGINT NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    -- NULL means "every repository in the namespace".
    repo_id      BIGINT REFERENCES repositories (id) ON DELETE CASCADE,
    url          TEXT NOT NULL,
    secret       TEXT NOT NULL,
    events       TEXT[] NOT NULL DEFAULT '{}',
    active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_webhooks_namespace ON webhooks (namespace_id);
CREATE INDEX IF NOT EXISTS idx_webhooks_repo ON webhooks (repo_id) WHERE repo_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id              BIGSERIAL PRIMARY KEY,
    webhook_id      BIGINT NOT NULL REFERENCES webhooks (id) ON DELETE CASCADE,
    event           TEXT NOT NULL,
    payload         JSONB NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'success', 'failed')),
    attempts        INT NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMPTZ,
    response_status INT,
    -- Truncated to a few KB so a chatty endpoint cannot bloat this table.
    response_body   TEXT NOT NULL DEFAULT '',
    -- The queue's lease/backoff clock: a claim pushes this forward so a
    -- crashed worker's in-flight row becomes claimable again on its own
    -- (see internal/webhooks), and a failed attempt pushes it forward by the
    -- exponential backoff interval before the next retry.
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_pending
    ON webhook_deliveries (next_attempt_at, id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_webhook
    ON webhook_deliveries (webhook_id, created_at DESC);

-- Daily download counters, used to answer "downloads in the last 30 days" on
-- a repository page without scanning history. Cumulative downloads keep
-- living on repositories.downloads; this table only needs to answer a
-- bounded time-window query cheaply. One count per resolve request advances
-- both, so this table is a window over that same total and can never exceed
-- it -- docs/dev/api-contract.md is authoritative for the rule.
CREATE TABLE IF NOT EXISTS repo_download_stats (
    repo_id BIGINT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    date    DATE NOT NULL,
    count   BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (repo_id, date)
);

CREATE INDEX IF NOT EXISTS idx_repo_download_stats_repo_date ON repo_download_stats (repo_id, date);
