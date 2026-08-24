# Deployment

This page is for whoever runs thinkingface: how to bring it up for evaluation, which
database and storage backends exist and when to pick each one, what production on GCP looks
like, and what happens (and does not happen) around upgrades and backups. See
[Configuration](configuration.md) for the full environment variable reference.

!!! warning "The network boundary is your only read boundary"

    thinkingface has no per-repository visibility setting. Every repository on an instance
    is readable, clonable and downloadable by anyone who can reach it, authenticated or
    not — accounts and organization roles govern writes only. Deploy it where only its
    intended audience can reach it, and treat network reachability as the access control it
    actually is.

## Local and evaluation deployment (Docker Compose)

The repository ships a `docker-compose.yml` that brings up the whole stack: the web UI, the
API, PostgreSQL, and a local GCS emulator.

```bash
cp .env.example .env
docker compose up -d
```

or equivalently, `make up`. This starts four services:

| Service | Image / build | Role |
|---|---|---|
| `web` | built from `frontend/` | Next.js UI, served with `next start` on port 3000 |
| `api` | built from `backend/` | The Go binary: HF-compatible REST API, git smart HTTP, LFS, the Parquet viewer, and the background sync worker, all in one process, on port 8080 |
| `postgres` | `postgres:17-alpine` | Metadata database (repositories, users, tokens, jobs, experiment runs) |
| `gcs` | `fsouza/fake-gcs-server` | A local emulator standing in for a real GCS bucket |

Once it is up:

- Web UI: <http://localhost:3000>
- API: <http://localhost:8080>
- Default login: `admin` / `admin` (see the warning below)

### Data persistence

Each stateful service writes to a named Docker volume:

| Volume | Holds |
|---|---|
| `pg-data` | PostgreSQL's data directory |
| `gcs-data` | The fake-gcs-server's backing filesystem (LFS objects, blobs) |
| `git-data` | Bare git repositories under `GIT_ROOT` (`/data`), plus the generated SSH host key |

These volumes survive a container restart or `docker compose down`.

### Stopping and resetting

```bash
docker compose down    # stop and remove containers; volumes are kept
make clean              # docker compose down -v -- also removes the named volumes
```

`docker compose down` (or `make down`) leaves your data in place, so `docker compose up -d`
afterwards picks up exactly where you left off. To start over from nothing — a fresh
database, an empty bucket, no repositories — run `make clean` (or `docker compose down -v`
directly), which deletes the named volumes along with the containers.

!!! warning "Change the defaults before exposing this to anyone else"
    A default `docker compose up` seeds an `admin` account with the well-known password
    `admin`, and signs session cookies with a publicly known development secret. This is
    fine on a laptop reachable only by you. Before putting the instance on a network anyone
    else can reach, set `TF_ADMIN_PASSWORD` and `TF_SESSION_SECRET` in `.env` — see
    [Configuration](configuration.md) for both. The server also refuses to start on these
    defaults at all once `TF_PUBLIC_URL` is an `https://` URL.

## Choosing a database backend

thinkingface reads a single `DATABASE_URL` environment variable and dispatches on its
scheme:

- `postgres://` or `postgresql://` — PostgreSQL
- `sqlite://` — SQLite (pure Go, via `modernc.org/sqlite`; no CGo)

A `docker compose up` (no override) always runs PostgreSQL, since `docker-compose.yml`
assembles `DATABASE_URL` from `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` and
passes it to the `api` service directly.

### SQLite mode

To bring the whole stack up against SQLite instead — no `postgres` container at all — use:

```bash
make up-sqlite
```

which runs `docker compose -f docker-compose.yml -f docker-compose.sqlite.yml up -d api web
gcs`. The `postgres` service is excluded, and the api container's `DATABASE_URL` becomes
`sqlite:///data/db/thinkingface.db`, persisted in a `sqlite-data` volume.

SQLite mode is a reasonable choice for evaluation, a single-operator instance, or a small
team, and it is what the production "SQLite + Litestream" option below is built on. It comes
with hard limits that make it the wrong choice past that scale:

- **A single process, a single writer connection.** Concurrent writes from multiple
  replicas are not supported — there is no clustering or replication story at the
  application layer.
- **No horizontal scaling.** You cannot run more than one `api` replica pointed at the same
  SQLite file.
- Search behaves differently from PostgreSQL: the HF-compatible `search=` substring match
  becomes a `LIKE`, which is case-insensitive for ASCII only (no Unicode case folding), and
  the web UI's full-text search runs on SQLite's FTS5 (`unicode61` tokenizer) rather than
  PostgreSQL's `tsvector`, so ranking and stemming behavior are not identical between the
  two.

Do not choose SQLite mode if you need multiple `api` replicas, if your namespaces or search
content rely on non-ASCII case folding, or if you need full-text search that matches
PostgreSQL's behavior exactly.

### PostgreSQL mode

PostgreSQL is the default for `docker compose up` and the right choice for anything beyond a
single-operator evaluation instance: it supports concurrent writers, standard row-level
locking, and (on Cloud SQL, in production) automated backups with point-in-time recovery. If
you are unsure which backend to pick, pick this one.

## Object storage

Large files (Git LFS objects) and non-LFS blobs published after a push are stored in an
object store, not in the git repositories on disk. `STORAGE_DRIVER` selects the
implementation:

- `gcs-emulator` — talks to `fake-gcs-server` at `STORAGE_EMULATOR_HOST`. This is what
  `docker compose up` uses locally. The emulator cannot verify signed URLs, so in this mode
  the server proxies object bytes itself rather than issuing them.
- `gcs` — talks to a real Google Cloud Storage bucket, named by `GCS_BUCKET` (optionally
  scoped under a `GCS_PREFIX`). In this mode the server issues short-lived signed URLs
  (`TF_SIGNED_URL_TTL`) and the client (browser, `huggingface_hub`, `git-lfs`) transfers
  directly against GCS.

### Credentials and permissions for real GCS

The `gcs` driver uses the standard Google Cloud Go client, which resolves credentials the
usual way — Application Default Credentials, meaning a mounted service account key
(`GOOGLE_APPLICATION_CREDENTIALS`), `gcloud auth application-default login` locally, or (in
production) the workload's attached service account. There is no thinkingface-specific
credential variable.

The service account needs, at minimum:

- `roles/storage.objectAdmin`, scoped to the target bucket (not project-wide)
- `roles/iam.serviceAccountTokenCreator` on itself — this lets it sign URLs (`signBlob`)
  without a downloadable private key, which is what makes keyless signed URLs work on a
  workload-identity-backed service account

Object storage has three top-level prefixes: `lfs/` (Git LFS objects), `blobs/` (every other
file the sync worker publishes after a push), and, when the Continuity migration is enabled
(see below), `wal/` (the git write-ahead log). All three are content-addressed, meaning
distinct pushes that produce the same file share storage.

## Production on GCP

The `infra/` directory holds Terraform for a GCP production deployment. It provisions:

- A GCS bucket for `lfs/`, `blobs/`, and (when applicable) `wal/`
- An Artifact Registry repository for the backend and frontend images
- A `google_cloud_run_v2_service` for the API (gen2, `h2c`, `min_instance_count = 1`, CPU
  always allocated, Direct VPC egress to reach the database)
- A `google_cloud_run_v2_service` for the web frontend
- A service account for the API workload, scoped to the bucket and to the secrets it needs
- Optionally, Cloud SQL for PostgreSQL 17 (private IP only, automated backups with
  point-in-time recovery)

Bring it up with:

```bash
cd infra
terraform init            # add -backend-config=... once you configure a real backend
terraform plan  -var="project_id=my-gcp-project"
terraform apply -var="project_id=my-gcp-project"
```

Terraform provisions the infrastructure but deliberately ignores drift on the container
image field afterwards, so pushing new images and pointing the Cloud Run service and job at
them is a separate step (`gcloud run deploy` / `gcloud run jobs update`). See
`infra/README.md` in the repository for the full apply-then-deploy walkthrough, including
how to wire a real backend for Terraform state.

Not provisioned by this Terraform: a custom domain or TLS front end. Cloud Run terminates
TLS itself and serves each service on its own `*.run.app` URL, which is enough to get
started; add a domain mapping or a load balancer once you have decided on a domain strategy.

### Database on GCP: Cloud SQL vs. SQLite + Litestream

Terraform's `database_backend` variable (`postgres`, the default, or `sqlite`) switches how
the API and its scheduled maintenance job persist metadata:

- **`postgres`** — a Cloud SQL for PostgreSQL 17 instance, private IP only, reached from
  Cloud Run over Direct VPC egress. `DATABASE_URL` is assembled and stored in Secret
  Manager. This is the configuration to pick when you need strict consistency across
  concurrent API instances.
- **`sqlite`** — no Cloud SQL instance is created at all. `DATABASE_URL` becomes a plain
  (non-secret) `sqlite:///data/db/thinkingface.db` pointing at the container's ephemeral
  filesystem, and `TF_LITESTREAM_REPLICA_URL` is set to a `gs://` path. The container's
  entrypoint (`backend/entrypoint.sh`) uses [Litestream](https://litestream.io) to restore
  that file from GCS on startup and continuously replicate writes back to it while the
  server runs, using the workload's own credentials — no extra key needed. Because SQLite
  assumes a single writer, the Cloud Run service's `max_instances` is forced to `1` in this
  mode regardless of the configured maximum.

  This avoids running Cloud SQL at all, which is attractive for a small-scale deployment,
  but it has a real caveat: a Cloud Run revision rollout can briefly run the old and new
  revision side by side, and in `sqlite` mode that means two writers to the same GCS replica
  for a short window during every deploy. Litestream does not reconcile multiple writers, so
  writes that land on the outgoing revision during that window can be lost. Mitigate this by
  deploying with `--no-traffic` and a manual traffic cutover, or accept the small window if
  deploys are infrequent. If you need strict consistency, use the Cloud SQL configuration
  instead.

### The Continuity / WAL migration

Recent Cloud Run compatibility for git itself rests on a design called the Continuity
migration (`TF_WAL_MODE` in [Configuration](configuration.md)): rather than requiring
persistent disk for bare git repositories, pushes are additionally written to a
generation-based write-ahead log in the GCS bucket (`wal/`), which can demote local disk to
a rebuildable warm cache. `docker-compose.yml` runs the API in `shadow` mode by default
(pushes are mirrored into the WAL best-effort, disk stays authoritative), and Terraform's
Cloud Run configuration depends on this migration to run without a persistent volume at all.
A daily Cloud Run Job (`compact`), triggered by Cloud Scheduler, performs WAL compaction.

## Upgrades and database migrations

Every `thinkingface` invocation that touches the database — including `serve` itself —
applies any pending SQL migration automatically on startup, before doing anything else.
Migrations are tracked by file name in a `schema_migrations` table, applying each one
exactly once and in order; re-running an already-applied migration is a no-op. This means a
plain image upgrade and restart (`docker compose up -d` after pulling a new image, or a
Cloud Run deploy) carries out the database migration as part of coming up — there is no
separate migration step to run by hand for the common case.

If you would rather apply migrations ahead of a rollout (for example, to keep a maintenance
window short), the same binary supports it directly:

```bash
docker compose run --rm api migrate
```

This applies pending migrations and exits without starting the server.

## Backup and restore

What the repository actually gives you differs by backend:

- **PostgreSQL on Cloud SQL**: automated daily backups with point-in-time recovery, provided
  by Cloud SQL itself and enabled by the Terraform configuration in `infra/`.
- **SQLite + Litestream on Cloud Run**: continuous replication of the SQLite file to GCS.
  Restoring means running `litestream restore` (the same command the container's entrypoint
  runs on startup) against the replica URL; there is no separate thinkingface-level restore
  command.
- **Object storage** (`lfs/`, `blobs/`, and `wal/` when Continuity is enabled): the
  Terraform configuration turns on bucket versioning, so old generations of the WAL's index
  and any overwritten object stay recoverable unless explicitly deleted. Deletion of
  orphaned LFS objects and blobs is handled by reference-counted garbage collection
  (`thinkingface gc`, with a `--dry-run` default), not by an age-based lifecycle rule, so
  nothing in the bucket disappears on its own. In `postgres` mode the Terraform configuration
  schedules this for you: a weekly Cloud Run Job (`gc`), triggered by Cloud Scheduler, offset
  from the `compact` schedule so the two never run at the same time. **In `sqlite` mode there is
  no gc Job** — it would read a Litestream-restored snapshot rather than the live database and
  could delete objects uploaded since that snapshot, so it is not created and the entrypoint
  refuses it; storage is not reclaimed automatically there. It only *reports* orphaned
  objects until you opt in — set `gc_delete_enabled = true` (Terraform variable) once you've
  reviewed a few dry-run reports and are confident they agree with what your deployment
  actually still references; see `infra/README.md` for the full rationale and how to trigger
  a supervised one-off deletion pass instead of waiting for the default weekly schedule.
- **Local Docker Compose deployment**: there is no backup story at all. Data lives in the
  `pg-data` / `sqlite-data`, `gcs-data`, and `git-data` named volumes, and nothing in the
  repository snapshots or ships them anywhere. If you need backups for a Compose-based
  deployment, you are responsible for backing up those Docker volumes yourself (for example,
  with a periodic `docker run --rm -v pg-data:/data ... tar` job); the repository does not
  provide one.

Bare git repositories on disk (or, once Continuity is enabled, the WAL) are the source of
truth for repository content outside of PostgreSQL/SQLite; back them up according to
whichever of the above applies to your deployment.

## See also

- [Configuration](configuration.md) for every environment variable, including the ones
  referenced above (`TF_ADMIN_PASSWORD`, `TF_SESSION_SECRET`, `DATABASE_URL`,
  `STORAGE_DRIVER`, `TF_WAL_MODE`, and the rest).
- [Authentication](../reference/authentication.md) for access tokens and SSH keys, once the
  instance is up.
