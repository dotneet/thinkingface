# Configuration

This is the full environment variable reference for thinkingface, for whoever is
configuring an instance. The authoritative source is `.env.example` at the repository root
— copy it to `.env` and adjust. Values below are the defaults the server falls back to when
a variable is unset.

!!! note "How `.env` reaches the containers under `docker compose`"
    `docker-compose.yml`'s `api` service loads `.env` two different ways at once: an
    `env_file:` entry reads it directly, and its `environment:` block also sets most of the
    same keys again, each as a `${VAR:-default}` expansion of the same top-level `.env`
    (which `docker compose` auto-loads for variable substitution, independently of
    `env_file:`). Where both define the same key, **`environment:` wins over `env_file:`**,
    so it's that second, `${VAR:-default}`-expanded layer that actually makes setting a
    value in `.env` take effect — a key present only via `env_file:` and not mirrored into
    `environment:` would be silently shadowed by nothing, which is why every tunable key is
    mirrored. A small number of settings (`GIT_ROOT`, `TF_GIT_HOOKS_PATH`) are hardcoded in
    `docker-compose.yml`'s `environment:` block as bare literals instead of
    `${VAR:-default}`, since they're tied to that file's own volume mount and baked-in image
    path rather than meant to be tuned per instance — setting them in `.env` has no effect
    under `make up` / `make up-sqlite`.

!!! warning "Change these before exposing an instance to anyone else"
    `TF_ADMIN_PASSWORD` and `TF_SESSION_SECRET` both ship with public, well-known defaults
    (`admin` and a fixed development string, respectively). Leaving either one unset is fine
    on a laptop only you can reach. **The server refuses to start on either default unless
    `TF_PUBLIC_URL` points at loopback** — `localhost`, a `.localhost` subdomain, or a literal
    loopback address. A LAN hostname or IP counts as reachable by someone else, whether or not
    TLS is in front of it, because knowing the default session secret is enough to forge a
    session cookie for any account.

!!! danger "Upgrading an existing plain-HTTP instance"
    This check used to apply only to `https://` URLs. An instance already running on something
    like `http://hub.internal` with the shipped defaults **will refuse to boot after upgrading**.
    Set `TF_ADMIN_PASSWORD` and a `TF_SESSION_SECRET` of at least 32 bytes before you upgrade.
    Changing `TF_SESSION_SECRET` signs everyone out — existing `tf_session` cookies no longer
    verify — but access tokens and SSH keys are unaffected.

## Server

| Variable | What it does | Default | Notes |
|---|---|---|---|
| `TF_ADDR` | Listen address for the HTTP API (git smart HTTP, LFS, REST, the viewer). | `:8080` | |
| `TF_PUBLIC_URL` | The externally reachable base URL of the API. Used to infer the CORS default origin and cookie security, and embedded in generated LFS/HF-compatible URLs. | `http://localhost:8080` | Pointing this anywhere other than loopback switches the server into "production" validation: it then refuses to boot on the default admin password or session secret. |
| `GIT_ROOT` | Directory holding bare git repositories on disk. | `/data/git` | Must be persistent storage unless the Continuity/WAL migration is `authoritative` (see the git/caching table below), in which case it is only a rebuildable cache. |

## Storage

| Variable | What it does | Default | Notes |
|---|---|---|---|
| `STORAGE_DRIVER` | Object storage backend: `gcs` (a real bucket) or `gcs-emulator` (fake-gcs-server). | `gcs-emulator` | Any other value fails startup. |
| `GCS_BUCKET` | Target bucket name. | `thinkingface` | |
| `GCS_PREFIX` | Optional key prefix inside the bucket, for sharing one bucket across environments. | *(empty)* | Leading/trailing slashes are stripped. |
| `STORAGE_EMULATOR_HOST` | Address of the fake-gcs-server emulator. | *(empty)* | Required when `STORAGE_DRIVER=gcs-emulator`; startup fails without it. Not used, and should be left unset, with `STORAGE_DRIVER=gcs`. |
| `TF_SIGNED_URL_TTL` | Floor of how long a signed GCS URL (LFS transfer, direct download) stays valid. The actual lifetime is derived from the object's size and clamped into `[TF_SIGNED_URL_TTL, TF_SIGNED_URL_MAX_TTL]`, so this mainly governs small transfers. | `1h` | Only meaningful with `STORAGE_DRIVER=gcs` — the emulator cannot verify signed URLs, so that mode proxies bytes through the server instead. Must be positive: startup fails on a zero or negative value, since it would otherwise mint URLs that are already expired the moment they're handed out, with no error to say so. |
| `TF_SIGNED_URL_MAX_TTL` | Ceiling of the same clamp, for large transfers. | `12h` | Same `STORAGE_DRIVER=gcs`-only caveat as `TF_SIGNED_URL_TTL`. Must not be negative, and — when set to a positive value — must not be shorter than `TF_SIGNED_URL_TTL`; either violation fails startup. `0` (or any non-positive value) instead means "no ceiling," leaving only the provider's own signing limit as the bound. |
| `TF_DEFAULT_STORAGE_QUOTA_BYTES` | Storage a namespace may use before uploads are refused, applied to every namespace without a quota of its own. `0` means unlimited. **It counts Git LFS objects only** — the same figure the storage page reports. A file pushed as an ordinary git object is still published to the bucket and is neither counted nor capped, so a repository whose `.gitattributes` does not route large files through LFS can exceed the intent of the quota. | `0` | Enforced when an LFS upload is requested: every upload object in a batch that would exceed the quota comes back with the per-object error `507` the LFS batch protocol defines (inside a `200`, which is how that protocol reports per-object failures), and `git push` / `huggingface_hub` surface its message. Per-namespace overrides are set by a site administrator under **Settings → Storage quotas**; lowering a quota never deletes anything, it refuses the next upload. **Deleting a file, replacing it, or deleting a whole ref does *not* reduce usage**: once a commit has ever named an LFS object, its link is kept for as long as the repository exists, so that object stays retrievable at any historical revision (`git checkout <old sha>`, `git lfs fetch --all`, `resolve` at an old ref) — the same guarantee `git clone` gives you. The only links ever released are ones no commit ever named at all: an LFS transfer that completed but whose commit never arrived (an interrupted `tf up` or `huggingface_hub` upload), and only after a roughly 24h grace period (`PruneRepoLFSLinks` in `backend/internal/store/files.go`). The only ways to actually shrink usage are deleting the whole repository, or rewriting history to drop the commits that reference the object and then running `thinkingface gc` — which is not available at all in SQLite mode, see [Deployment](deployment.md#choosing-a-database-backend). |

## Database

| Variable | What it does | Default | Notes |
|---|---|---|---|
| `DATABASE_URL` | Connection string. The scheme selects the backend: `postgres://` / `postgresql://` for PostgreSQL, `sqlite://` for SQLite. | *(none — required)* | Startup fails if this is unset or uses any other scheme. See [Deployment](deployment.md) for the tradeoffs between the two. Under `docker compose up`, this is assembled from `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` below rather than read from `.env` directly. |
| `POSTGRES_USER` | PostgreSQL role name, used by the `postgres` container and to assemble `DATABASE_URL` in Compose. | `tf` | Compose-only; not read by the server itself. |
| `POSTGRES_PASSWORD` | PostgreSQL role password. | `tf` | Compose-only. Change this alongside `TF_ADMIN_PASSWORD` for anything beyond local use. |
| `POSTGRES_DB` | PostgreSQL database name. | `thinkingface` | Compose-only. |
| `POSTGRES_PORT` | Host port PostgreSQL is published on, for connecting from outside the Compose network (`make test-store-pg`). | `5432` | Compose-only. |
| `TF_LITESTREAM_REPLICA_URL` | `gs://` destination Litestream replicates the SQLite file to/from. | *(unset)* | Only relevant to a production SQLite deployment on Cloud Run (see [Deployment](deployment.md)); leave unset locally — the Compose SQLite mode gets by with a plain volume. |

## Authentication and sessions

| Variable | What it does | Default | Notes |
|---|---|---|---|
| `TF_ADMIN_USERNAME` | Username seeded for the first account, only when the users table is empty. | `admin` | |
| `TF_ADMIN_PASSWORD` | Password for that seeded account. | `admin` | **Change this.** Well-known default; startup is refused whenever `TF_PUBLIC_URL` points anywhere other than loopback while it is unset. **First boot only** — see the note below. |
| `TF_ADMIN_EMAIL` | Email address for the seeded account. | `admin@example.com` | |
| `TF_SESSION_SECRET` | HMAC-SHA256 key signing session cookies (`tf_session`) and LFS transfer URLs. | `dev-insecure-session-secret` | **Change this.** Must be at least 32 bytes, and must not be the default, whenever `TF_PUBLIC_URL` points anywhere other than loopback. Anyone who knows the default can forge a session cookie for any account, so an instance on a LAN name needs a real one just as much as a public one does. |
| `TF_SESSION_TTL` | How long an issued session cookie stays valid. | `168h` (7 days) | Also invalidated early on logout or password change. |
| `TF_COOKIE_SECURE` | Force the `Secure` attribute on the session cookie. | *(inferred from `TF_PUBLIC_URL`)* | Set explicitly to `true` when TLS terminates in front of the server (e.g. a load balancer) and traffic to the container itself is plain HTTP — the automatic inference gets this wrong in that setup. |
| `TF_ALLOWED_ORIGINS` | Comma-separated browser origins allowed for credentialed CORS. | *(inferred: `TF_PUBLIC_URL`'s origin, plus `http://localhost:3000` / `http://127.0.0.1:3000` when not `https`)* | Set this explicitly in production if the web UI is served from a different host than the API — an origin outside the allowlist gets no CORS headers and its state-changing cookie-authenticated requests are rejected with 403. `huggingface_hub`, `git`, and `curl` send no `Origin` header and are unaffected either way. |
| `TF_AUTH_RATE_LIMIT_PER_MIN` | Failed password attempts allowed per client IP per minute (half that per username). `0` disables it. | `10` | Applies to both the login endpoint and HTTP Basic auth (accepted on every route). Counted per process — with multiple replicas, the limit applies per replica, not globally. |
| `TF_TRUST_PROXY_IPS` | Read the client IP from the leftmost `X-Forwarded-For` entry for rate limiting. | `false` | Only enable this when a proxy you control overwrites that header; otherwise a client can pick its own rate-limit bucket. |
| `TF_ALLOW_SIGNUP` | Whether self-service account creation is open. | `true` | Set to `false` to close the public **Sign up** tab. Not a one-way door: a site administrator can still add accounts at **Settings → Users** (`/settings/admin/users`), which ignores this flag by design. |
| `TF_SIGNUP_EMAIL_DOMAINS` | Comma-separated email domains self-service signup accepts. | *(empty — no restriction)* | Matched case-insensitively and **exactly**: `example.com` admits `alice@example.com` and refuses `alice@sub.example.com`, so list a subdomain separately if it should be allowed. A refused address is told which domains are accepted. Applies to the public signup form only — **Settings → Users** ignores it, like `TF_ALLOW_SIGNUP`. |
| `TF_SIGNUP_REQUIRE_APPROVAL` | Hold self-service signups until an administrator approves them. | `false` | The account is created but authenticates on **nothing** — not its password, not an access token, not an SSH key — and no session is issued; the person is told they are waiting. Approve at **Settings → Users**, where pending accounts sort to the top. Accounts created by an administrator, and every account that existed before this was turned on, are already approved. |
| `TF_ORG_CREATION` | Who can create organizations: `anyone` or `admin`. | `anyone` | Any other value fails startup. |

!!! warning "`TF_ADMIN_PASSWORD` only applies to the first boot"
    The three `TF_ADMIN_*` variables seed the initial account and are read **only while the
    users table is empty**. Once any account exists the seeding step returns immediately, so
    editing `TF_ADMIN_PASSWORD` and restarting changes nothing.

    After first boot, accounts and passwords are managed in the web UI: each user changes their
    own password at **Settings → Account** (`/settings/account`), and a site administrator can
    add an account, reset anyone's password, or appoint a second administrator at
    **Settings → Users** (`/settings/admin/users`). See
    [Authentication](../reference/authentication.md#changing-your-password). Appointing a second
    administrator early is worth doing: it is what keeps the instance recoverable if the seeded
    one loses its password.

## SSH

| Variable | What it does | Default | Notes |
|---|---|---|---|
| `TF_SSH_ENABLED` | Turns on the git-over-SSH listener. | `true` in Compose (`false` in the server's own default) | Opens a second port; needs a host key on persistent storage, so this is treated as an explicit deployment decision. |
| `TF_SSH_ADDR` | Listen address for the SSH server. | `:2222` | Required (non-empty) when `TF_SSH_ENABLED=true`. |
| `TF_SSH_PUBLIC_PORT` | The port clients should dial, when it differs from the one `TF_SSH_ADDR` binds. | *(the port from `TF_SSH_ADDR`)* | Set this whenever a port mapping or a load balancer publishes the listener elsewhere — compose and Kubernetes both do. It is what the repository page shows as the SSH clone URL, so a wrong value hands every user a URL that does not connect. `22` is rendered implicitly (`ssh://git@host/…`). |
| `TF_SSH_HOST_KEY_PATH` | Where the server's SSH host key lives. | `/data/ssh/host_ed25519` | Generated on first start if missing. **Must be on persistent storage** — on an ephemeral filesystem, every restart mints a new identity and every client sees a host key mismatch warning. |
| `TF_SSH_IDLE_TIMEOUT` | Closes an SSH connection that has gone quiet. | `10m` | `0` disables it. Only reaps abandoned connections — active clones keep streaming regardless. |
| `TF_SSH_PORT` | Host port the SSH listener is published on. | `2222` | Compose-only (`docker-compose.yml` port mapping); not read by the server itself. |

Clients connect with public-key auth only, using keys registered in the web UI at
`/settings/ssh-keys`:

```bash
git clone ssh://git@localhost:2222/admin/imdb-reviews.git
```

## Experiments

| Variable | What it does | Default | Notes |
|---|---|---|---|
| `TF_EXP_FLUSH_INTERVAL` | How long the native ingest API's metric points may stay database-only before the sync worker commits them into the dataset repository's Parquet file. `0` disables the flush (points stay database-only). | `1m` | A run that reaches `finished` or `failed` is always flushed immediately regardless of this value. |

## Git, caching, and the Continuity/WAL migration

| Variable | What it does | Default | Notes |
|---|---|---|---|
| `TF_SYNC_WORKERS` | Number of concurrent workers processing post-push jobs (publishing blobs, updating the metadata index). | `2` | Workers run different refs in parallel; jobs for one repository and ref always run one at a time, in order, so raising this is safe. |
| `TF_VIEWER_CACHE_DIR` | Local working directory for WAL compaction. The Parquet viewer reads objects directly from storage via range requests and no longer caches to disk here. | `/data/cache` | |
| `TF_VIEWER_METADATA_CACHE_BYTES` | Byte budget for the Parquet viewer's in-process footer/metadata cache. | `268435456` (256 MiB) | This is process heap memory, not disk — it does not compete with `TF_VIEWER_CACHE_DIR`'s tmpfs budget. |
| `TF_WAL_MODE` | How far the Continuity migration has progressed: `off`, `shadow`, or `authoritative`. | `off` (`shadow` under Compose) | `off`: on-disk repositories under `GIT_ROOT` are the source of truth. `shadow`: pushes are also mirrored into a GCS-backed write-ahead log, best-effort — disk stays authoritative and a WAL failure never fails a push. `authoritative`: the WAL is the truth, reads materialize from it, and `GIT_ROOT` becomes a bounded, rebuildable cache. Any other value fails startup. |
| `TF_GIT_HOOKS_PATH` | `core.hooksPath` directory baked into the image, wiring pushes through the WAL. | *(empty)* | **Required whenever `TF_WAL_MODE` is not `off`** — startup fails otherwise, since without it pushes over git smart HTTP would silently bypass the WAL. Compose sets this to `/opt/thinkingface/hooks`. |
| `TF_GIT_CACHE_BYTES` | Byte budget for the materialized-repository cache under `GIT_ROOT` when `TF_WAL_MODE=authoritative`. | `2147483648` (2 GiB) | `0` disables eviction. Unused when the WAL is not authoritative. |

## Frontend

| Variable | What it does | Default | Notes |
|---|---|---|---|
| `NEXT_PUBLIC_API_URL` | The API base URL the **browser** uses. Compiled into the client bundle at `docker build` time — it is *not* read from the container's environment at startup, so a runtime `environment:` entry for it has no effect on the browser bundle (Compose also passes it as a build `arg` for exactly this reason). | `http://localhost:8080` | Must be an address the browser itself can reach — not the internal Compose network name. Changing `.env` and re-running `docker compose up -d` alone does **not** pick up a new value: rebuild the image first with `docker compose up -d --build web`. |
| `API_URL` | The API base URL Next.js **Server Components and route handlers** use, from inside the container. | *(unset — falls back to `NEXT_PUBLIC_API_URL`)* | Not listed in `.env.example`: `docker-compose.yml` sets it directly to `http://api:8080` in the `web` service's environment, since it is an internal, service-to-service address rather than something an operator tunes per deployment. Set it directly on the container if you run the frontend outside of this Compose file. |

!!! note "Why two API URLs?"
    Inside `docker compose`, the `web` container reaches the API at `http://api:8080` (the
    internal service name), but a browser loading pages from that container cannot resolve
    `api` at all — it needs the URL the host actually publishes, `http://localhost:8080`.
    `API_URL` covers the first case (server-side rendering and route handlers, which run
    inside the container); `NEXT_PUBLIC_API_URL` covers the second (anything that runs in
    the browser). Deploying behind a real domain works the same way: point `API_URL` at
    whatever address the frontend's own runtime can reach the backend on, and
    `NEXT_PUBLIC_API_URL` at the public address end users' browsers will call.

## Variables set outside `.env.example`

A few environment variables the server reads are not listed in `.env.example` because
`docker-compose.yml` does not need to vary them for a typical local setup. They still work
if you set them directly (for example, in a standalone deployment or a
`docker-compose.override.yml`):

| Variable | What it does | Default |
|---|---|---|
| `TF_WEBHOOK_WORKERS` | Number of concurrent workers delivering repository/organization webhooks. | `1` |
| `TF_WEBHOOKS_ALLOW_PRIVATE_TARGETS` | Opts out of the webhook SSRF guard, allowing a webhook target on localhost or a private network. | `false` — leave this at its default outside local development. |

## See also

- [Deployment](deployment.md) for how these variables come together in Docker Compose,
  SQLite vs. PostgreSQL, and the GCP production setup.
- [Authentication](../reference/authentication.md) for access tokens, once `TF_ADMIN_USERNAME`
  / `TF_ADMIN_PASSWORD` have gotten you logged in.
