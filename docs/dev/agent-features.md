# Agent-facing and private-deployment features

Design notes for the batch of changes that came out of running thinkingface privately for a
day of experiment tracking driven by an AI agent (the 2026-09-27 improvement proposal). This is
the shared contract the implementation was split along; the per-endpoint reference lives in
`api-contract.md` §7 (experiments) and §1 (tokens), and the user-facing story in
`docs/users/`.

The proposal's items are referenced by number (P1..P18). Two were deliberately left out:

- **P6, prebuilt images** — publishing is a release-process decision, not code. The Web UI
  image is now generic (P3), which is the prerequisite.
- **P2, a loopback-only override example** — made unnecessary by P1.

## 1. Private deployment

### 1.1 Published ports (P1)

`docker-compose.yml` / `docker-compose.sqlite.yml`:

| Service | Mapping | Notes |
|---|---|---|
| postgres | `127.0.0.1:${POSTGRES_PORT:-5432}:5432` | always loopback: only `make test-store-pg` needs it from the host |
| gcs | `127.0.0.1:4443:4443` | always loopback: `scripts/gcs-host-proxy.py` and the E2E bucket checks need it; fake-gcs-server has no auth |
| api | `${TF_BIND_ADDR:-127.0.0.1}:8080:8080` | |
| api (ssh) | `${TF_BIND_ADDR:-127.0.0.1}:${TF_SSH_PORT:-2222}:2222` | |
| web | `${TF_BIND_ADDR:-127.0.0.1}:3000:3000` | |

`TF_BIND_ADDR=0.0.0.0` restores LAN reachability. Loopback by default because Docker's
published ports bypass the host firewall.

### 1.2 Same-origin API proxy (P3, P5)

The browser used to call the API at `NEXT_PUBLIC_API_URL`, which Next.js inlines at build time,
so changing the port the API is reached on meant rebuilding the web image, and a web UI on a
non-default port needed `TF_ALLOWED_ORIGINS`.

Now, when `NEXT_PUBLIC_API_URL` is **empty at build time** (the new default for the compose
build and the Dockerfile), the browser calls the API on its own origin, and the Next.js server
forwards those requests to `API_URL` (read at *runtime*):

- `frontend/app/api/[...path]/route.ts` proxies every method under `/api/*` (the whole API
  surface the browser uses lives there; HF `resolve` paths that the UI links to are rewritten
  to `/api/v1/raw/...` or proxied the same way — see the route file).
- Bodies are streamed both ways (`duplex: "half"`), never buffered: Web UI uploads and parquet
  range reads go through it. `Range`, `If-None-Match`, `Set-Cookie` and the exposed headers
  pass through; hop-by-hop headers and `Content-Encoding`/`Content-Length` of a body `fetch`
  already decoded do not.
- CSRF: the API's `requireSameOrigin` sees the browser's `Origin`, which is the web UI's
  origin. The proxy checks, for unsafe methods, that `Origin` (or `Referer`) matches the
  request's own `Host`, answers 403 otherwise, and then **drops** `Origin`/`Referer` before
  forwarding — the check has already been made by the only party that can make it.
- The client address: a route handler cannot see the client socket, and Next.js only fills in
  `X-Forwarded-For` when the client sent none -- a client-supplied value is passed through
  unchanged -- so that header proves nothing about the browser. With one address for every
  browser, the password rate limiter let one visitor lock every browser out of signing in.
  Instead:
  - the production entry point is a custom server, `frontend/server.ts` (`bun run start`),
    which pins `X-TF-Client-Addr` to the socket peer before Next.js runs -- or, with
    `TF_WEB_TRUSTED_PROXY_HOPS=N`, to the entry N places from the right of `X-Forwarded-For`
    plus the peer -- and discards any client-supplied copy (`lib/client-addr.ts`);
  - the proxy forwards that header only when it runs under that server (`TF_WEB_EDGE=1`, set
    by the server on its own process), adding `X-TF-Proxy-Secret` from `TF_WEB_PROXY_SECRET`;
    under `next dev` / `next start` both are dropped;
  - the API believes `X-TF-Client-Addr` only when the connection peer is in
    `TF_TRUSTED_WEB_PROXIES` (IPs, CIDRs, or host names re-resolved every 30 s; compose sets
    `web`) or the secret matches (`backend/internal/api/webproxy.go`). A peer list is what
    makes the zero-config compose case safe: a direct caller of the published API port arrives
    from Docker's gateway, not the web container. The secret covers Cloud Run, where the peer
    is Google's front end; the Terraform generates it into Secret Manager for both services.
  `TF_TRUST_PROXY_IPS` still reads `X-Forwarded-For` for proxies in front of the API itself.
- A non-empty `NEXT_PUBLIC_API_URL` keeps the old cross-origin behaviour byte for byte.

### 1.3 Startup summary and CORS diagnostics (P4, P5)

`thinkingface serve` logs one `slog.Info("effective configuration", ...)` line at startup:
public URL, listen addresses, allowed origins, database driver (never the DSN), storage driver
and bucket/emulator, WAL mode, signup/approval/org-creation policy, SSH on/off,
`require_auth_for_read`, and `session_secret_default` / `admin_password_default` booleans
(never the values).

The CORS middleware logs `slog.Warn("cors: origin not allowed", "origin", ...)` the first time
it refuses a given origin (a bounded set, 64 entries, so a scanner cannot grow it without
limit), naming `TF_ALLOWED_ORIGINS` in the message.

### 1.4 Non-interactive admin token (P6)

```
thinkingface admin token create <username> [--name NAME] [--scope read|write]
    [--expires-in-days N] [--repo KIND/NS/NAME ...] [--output FILE]
```

Runs out of process against the database like `admin passwd`. Writes the plaintext token to
`--output` (created `0600`, refused if it exists) or stdout, and nothing else to stdout.

### 1.5 Authenticated reads (P10)

`TF_REQUIRE_AUTH_FOR_READ=true` makes every route answer `401` (`type:
"authentication_required"`, plus `WWW-Authenticate: Basic realm="thinkingface"`) to a caller
with no identity, except:

- `GET /healthz`
- `POST /api/v1/auth/login`, `POST /api/v1/auth/signup`, `POST /api/v1/auth/logout`
- `GET /api/v1/me` (already 401 for a signed-out caller; listed so its error stays the usual one)
- `GET /api/v1/server-info` (below)

`GET /api/v1/server-info` → `apitypes.ServerInfo`: `{require_auth_for_read, allow_signup}`,
which the web UI reads to redirect a signed-out visitor to `/login?next=...` instead of
rendering a wall of errors.

## 2. Experiments

### 2.1 Run summaries with min / max (P12)

`exp_runs` gains `summary_min` and `summary_max` (JSON objects, metric → number), maintained
exactly like `summary` (last value): merged at ingest (min/max of stored and batch), recomputed
from the parquet by the indexer. `apitypes.ExpRun` exposes them as `summary_min` /
`summary_max`.

### 2.2 Metric goals (P12)

`exp_projects.metric_goals` (JSON object, metric → `"min"` | `"max"`). Set with

```
PATCH /api/v1/experiments/{ns}/{repo}/{project}
{"metric_goals": {"cvl_val/CER": "min", "acc": "max", "old_metric": ""}}
```

A key mapped to `""` removes the goal; keys not mentioned are left alone. Write access to the
repository is required. Answers `apitypes.ExpProject` (which now carries `metric_goals`). The
project row is created if it does not exist yet, so goals can be declared before the first run.

### 2.3 Querying runs (P12)

`GET /api/v1/experiments/{ns}/{repo}/{project}/runs` keeps its response shape and gains
optional query parameters, all applied server-side:

| Parameter | Meaning |
|---|---|
| `group` | exact sweep group; repeatable (any of) |
| `status` | derived status (`running` / `finished` / `failed` / `stale`); repeatable (any of) |
| `tag` | repeatable; a run must carry every tag given |
| `archived` | `true` / `false`; absent = both (the pre-existing behaviour) |
| `sort` | `name`, `started_at`, `updated_at`, `last_step`, `last:<metric>`, `min:<metric>`, `max:<metric>`, `best:<metric>`, `config:<key>` (dotted path into nested config) |
| `order` | `asc` / `desc`. Default `asc`, except `best:` (best first regardless) |
| `limit` | 1..1000 |

Runs lacking the sort value always sort last. `best:<metric>` needs a goal for the metric
(400 otherwise) and sorts by `summary_min` for `"min"`, `summary_max` for `"max"`.

`apitypes.ExpRunListResponse` gains `metric_goals` (the project's goals) and `best` (metric →
name of the best *non-archived* run among the filtered runs, for every metric with a goal
that at least one run has).

### 2.4 One run, and waiting on it (P13)

```
GET /api/v1/experiments/{ns}/{repo}/{project}/runs/{run}
    [?wait=<duration, max 60s>&since=<updated_at RFC 3339>&status=<derived status>]
```

Answers `apitypes.ExpRunResponse{run}`. With `wait`, the handler holds the request until the
run's `updated_at` is later than `since`, **or** its derived status differs from `status`
(this is what makes a run going stale end a wait), or `wait` elapses; it answers 200 with the
current run in every case, so the client compares and decides. A run that does not exist
answers 404 immediately (the CLI keeps polling until its own timeout, so an agent can start
waiting before the training job has logged anything). Polls the store at most once a second.

### 2.5 Config diff (P12)

```
GET /api/v1/experiments/{ns}/{repo}/{project}/config-diff?run=a&run=b[&include_meta=true]
```

No `run` means every non-archived run (at most 200). Configs are flattened to dotted paths;
`_meta` and `_resume` are left out unless `include_meta=true`. Answers
`apitypes.ExpConfigDiffResponse{runs, keys: [{key, values: {run: value}}]}` with only the keys
whose value differs (or is missing) across the runs, sorted by key.

### 2.6 Heartbeats and staleness (P14)

`POST .../log` accepts an optional `heartbeat_secs` (1..3600). It is stored on the run
(`exp_runs.heartbeat_secs`, 0 = the client never declared one) and exposed on `ExpRun`.

The Python shim sends it on every batch and, when a run has sent nothing for
`heartbeat_secs` (30 by default), posts a batch with `"points": []` purely as a liveness ping.

`deriveRunStatus` uses it: a `running` run with `heartbeat_secs > 0` is `stale` once
`now - updated_at > max(4 × heartbeat_secs, 2 min)`; without one the old 30-minute window still
applies, so runs logged by older clients do not flicker.

Only ingest writes (`/log`, pings included, and `/finish`) move `updated_at`
(`store.ExpRunUpsert.Touch`); the parquet indexer, which re-upserts every run of the repository
on every flush and push, moves it only when the parquet reaches a higher `last_step`. Otherwise a
crashed run would be kept looking alive by its siblings' flushes. A ping also never moves a
`finished` / `failed` run back to `running`, and leaves the stored summaries untouched.

### 2.7 Project notes (P15)

Run notes and tags already existed (`PATCH .../runs/{run}`). The project gets an experiment
notebook, stored **in the repository** as `{project}/NOTES.md` on the default branch so that
`git clone` keeps it with the parquet:

```
GET /api/v1/experiments/{ns}/{repo}/{project}/notes
PUT /api/v1/experiments/{ns}/{repo}/{project}/notes  {"content": "...", "base_sha": "<blob sha or empty>", "message": "..."}
```

`apitypes.ExpNotesResponse{path, content, exists, blob_sha, commit_sha}`. `GET` of a project
with no notes answers 200 with `exists: false`. `PUT` commits to the default branch; when
`base_sha` is present and differs from the current blob (`""` meaning "the file must not
exist yet"), it answers 409 so a concurrent edit is not silently overwritten. Omitting
`base_sha` overwrites. Content is capped at 256 KiB.

Links of the form `[text](run:<run name>)` in the notes are rendered by the Web UI as links to
that run in the same project.

### 2.8 Media and artifacts during a run (P16)

Python only — the server already serves everything through the artifact listing and the
repository file browser.

- `trackio.Image(value, caption=None)` (a path, a PIL image, or a HxW / HxWxC numpy array)
  logged as a metric value is saved as PNG and committed as the artifact
  `media/{key}/step_{step:08d}.png`; the point itself carries no value for that key.
- `trackio.Table(dataframe=None, columns=None, data=None)` is written as parquet (pyarrow or
  pandas required, a warning otherwise) to `tables/{key}/step_{step:08d}.parquet`, which the
  parquet viewer and its SQL console can open.
- Staged artifacts and media are no longer held until `finish()`: they are committed in the
  background every `THINKINGFACE_ARTIFACT_INTERVAL` seconds (default 60) when anything is
  pending, and `trackio.save()` commits immediately. `finish()` still commits the remainder.

### 2.9 Offline runs and sync (P9)

`THINKINGFACE_MODE=offline` (or `init(mode="offline")`) makes the shim write a run to disk
instead of the network. The online mode also writes to disk, instead of dropping, the points it
would otherwise have given up on (a full retry buffer, or `finish()` exhausting its retries).
`tf experiments sync` replays such directories.

Directory: `THINKINGFACE_OFFLINE_DIR` (default `./thinkingface-offline`). One subdirectory per
run: `{UTC yyyymmddTHHMMSS}-{slug(project)}-{slug(run)}-{8 hex}` where `slug` keeps
`[A-Za-z0-9._-]` and replaces anything else with `_`, capped at 40 bytes.

Inside:

- `run.jsonl` — append-only, one JSON object per line, flushed after every line. A line that
  does not parse (the process died mid-write) ends the file for the reader.
- `artifacts/` — copies of the files `log_artifact` / media staged, at their artifact name.
- `sync-state.json` — written only by the syncer (atomically: temp file + rename).

`run.jsonl` records (`"v": 1` on every record):

```json
{"v":1,"type":"init","time":"...","repo":"ns/name"|null,"project":"p","run":"name","resume":"never|allow|must","group":"","job_type":"","config":{...}}
{"v":1,"type":"log","points":[{"step":0,"timestamp":"...","metrics":{"loss":0.5}}],"config":{...}}
{"v":1,"type":"artifact","name":"media/samples/step_00000010.png","path":"artifacts/media/samples/step_00000010.png"}
{"v":1,"type":"model","repo_id":"ns/name","revision":""}
{"v":1,"type":"finish","time":"...","status":"finished|failed"}
```

- `init` is always the first line. `repo: null` means "resolve `{user}/trackio-metrics` at sync
  time" — written by an offline run without `THINKINGFACE_REPO`, and also by a spill whose repo
  was never resolved (`THINKINGFACE_REPO` unset and `GET /api/v1/me` failed at `init()`, so the
  online run only had the `unknown/trackio-metrics` placeholder). `resume` follows the online
  semantics; a spill file from the online mode always says `"allow"` because it continues a run
  the server already has. Offline, `resume` does **not** continue step numbering: the shim
  cannot know the server's `last_step`, so auto-numbered steps start at 0 (the shim warns once)
  and the syncer does not shift them — an offline run that continues an existing one passes
  explicit `step=`.
- `log.config` is present only when the config changed since the previous record (the first
  `log` always carries it). A spill (online mode) omits it — and writes `init.config: null` —
  once the current config was already delivered online, so replaying the spill cannot roll the
  server's config back; a config that never reached the server is included as usual. The
  syncer sends a config only when a record carries a non-null one.

`sync-state.json`: `{"v":1,"synced_lines":N,"run":"<name actually used on the server>","repo":"ns/name","base_config":{...},"done":bool}`.
`base_config` is the stored config of a run the directory continues (`resume` allow/must).

`tf experiments sync [DIR ...] [--watch] [--interval 60s] [--json]` (default DIR:
`./thinkingface-offline`, each argument may be the parent or one run directory):

1. While the state has no run name yet it resolves the repo and the run name with the same
   `resume` rules as the shim (`never` + name taken → `name-1`, ...) and persists them with
   `synced_lines: 0` before sending anything, so the `init` record (line 0, the carrier of the
   initial config) is re-processed until a `/log` delivers it.
2. Replays records after `synced_lines`: consecutive `log` records are coalesced into `/log`
   calls of at most 10 000 points; `synced_lines` is persisted after each successful call.
   Every config sent on a continued run is `base_config` merged under the record's config; a
   `null` / absent config sends none. When the directory has a `finish` record, the replayed
   batches carry its status instead of `running`, so replaying a run whose online `/finish`
   already succeeded never flips it back. A complete line that fails to decode into a record
   is skipped with a warning; only a syntax error or an unterminated line ends the file.
3. At `finish` it commits the staged artifacts in one commit (a file whose content — git blob
   / LFS oid — is already at that path is skipped, which is also what keeps a crash between
   commit and state write from duplicating), writes produced models, posts `/finish`, and
   marks `done`.
4. A directory without a `finish` record is synced up to its end and left not-done, so
   `--watch` keeps following a live offline run.

Delivery is at-least-once per `/log` call: a crash between the call and the state write
re-sends that call's points.

## 3. Tokens restricted to repositories (P17)

`POST /api/v1/tokens` accepts `"repos": ["datasets/alice/exp", "models/alice/ocr"]` (plural
kind + `/ns/name`, at most 32). `apitypes.TokenItem` gains `repos` (empty = unrestricted).
Stored in `access_token_repos`.

A restricted token:

- may perform repository-scoped **writes** (push, commit, LFS upload, ingest, annotations,
  notes, metric goals, file edits) only on the listed repositories;
- may not perform **admin-level** repository operations anywhere (delete, transfer, rename,
  archive, settings, webhooks);
- may not perform **account-level** writes (create repositories, mint or revoke tokens, SSH
  keys, profile, password, organisations, admin endpoints) — 403
  `type: "token_restricted"`;
- reads exactly like an unrestricted token of the same user, except admin-level reads such as
  webhook configuration (refused like the other admin operations).

The per-request access log line carries `token_id` (and the audit lines for token lifecycle
already carry `token_name`), so an agent's actions are attributable. The Web UI's token form
gets an optional repository list.

## 4. `tf` CLI (P8, P9, P11, P12, P13, P15, P18)

Every command gains `--json` (machine-readable stdout, errors still on stderr with the exit
code). New:

```
tf experiments runs    REPO PROJECT [--group G] [--status S] [--tag T] [--archived true|false] [--sort SPEC] [--order asc|desc] [--limit N] [--columns config:lr,min:loss,...] [--json]
tf experiments run     REPO PROJECT RUN [--json]
tf experiments wait    REPO PROJECT RUN [--until EXPR] [--timeout 2h] [--json]
tf experiments diff    REPO PROJECT [RUN ...] [--include-meta] [--json]
tf experiments goals   REPO PROJECT [METRIC=min|max|none ...] [--json]
tf experiments notes   REPO PROJECT [--set FILE|-] [--json]
tf experiments annotate REPO PROJECT RUN [--note TEXT|--note-file FILE] [--tag T ...] [--archive|--unarchive] [--json]
tf experiments import  REPO PROJECT FILE... [--configs FILE] [--status finished] [--replace] [--json]
tf experiments sync    [DIR ...] [--watch] [--interval 60s] [--json]
tf mcp                 (stdio MCP server)
```

`REPO` is `ns/name` (a dataset repository).

`--until` grammar: `cond (('and'|'or') cond)*`, `and` binding tighter, parentheses allowed;
`cond := IDENT OP VALUE` with `IDENT` ∈ `step` (last_step), `status` (derived), `points`,
`metric:<name>` (last value), `min:<name>`, `max:<name>`; `OP` ∈ `== != >= <= > <`. Default
`status!=running`. `wait` exits 0 when the condition holds, 1 on timeout (printing the last
state), 2 on usage errors, and uses the long-poll endpoint in a loop.

`import`: FILE is CSV (header row) or JSONL; each row has `run`, `step`, optional `timestamp`
and numeric metric columns (non-numeric / empty cells are skipped). `--configs` is JSONL of
`{"run", "config", "status", "group", "job_type"}`; a run without an entry is finished with
`--status`. `--replace` deletes an existing run of the same name first; without it an existing
run is refused (importing twice would duplicate points).

`tf mcp` speaks MCP (JSON-RPC 2.0, newline-delimited, stdio; protocol `2025-06-18`) and uses
the same credentials as the rest of `tf`. Tools: `list_experiment_repos`, `list_projects`,
`list_runs`, `get_run`, `get_metrics`, `wait_for_run`, `config_diff`, `get_notes`,
`update_notes`, `annotate_run`, `set_metric_goals`.

## 5. OpenAPI (P11)

`GET /api/openapi.json` serves an OpenAPI 3.1 document for the programmatic surface: auth,
tokens, `me`, repository listing/creation, and all of `/api/v1/experiments`. It is a
hand-maintained file embedded in the binary (`backend/internal/api/openapi.json`); a test
walks the chi router and fails when a documented path/method is not routed, or when a routed
`/api/v1/experiments` or `/api/v1/tokens` path is not documented.
