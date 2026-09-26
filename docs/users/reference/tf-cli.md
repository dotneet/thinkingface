# tf CLI

`tf` is a single static-binary command-line client for registering datasets and models with
thinkingface, and for querying and managing experiment runs. This page is the complete reference
for its commands, flags, credential resolution, and configuration file. For a first walkthrough, see
[Uploading Files](../guides/uploading.md); this page assumes you already know why you'd
reach for `tf` and want the exact details.

```bash
tf login http://localhost:8080   # once only
tf up ./imdb-ja                  # everything else is just this
```

`tf` has no protocol of its own — it is a thin client over the same HF-compatible HTTP API
(`whoami` / `create_repo` / `preupload` / LFS batch / `commit`) that `huggingface_hub` uses, so
anything `tf up` does could also be done with `hf upload`. See
[Relationship to `hf upload`](#relationship-to-hf-upload) below.

## Installation

If you have Go 1.25 or later:

```bash
go install github.com/dotneet/thinkingface/backend/cmd/tf@latest
```

From a checkout of the repository, you can also build it locally:

```bash
make tf   # builds to backend/bin/tf
```

`make tf` embeds a version string from `git describe`, which `tf version` then prints.

## Quick start

```bash
# 1. Log in to the server (issues and saves a single token)
tf login http://localhost:8080

# 2. Register a directory as-is
#    - Kind is inferred from file contents (model if safetensors/config.json exist,
#      otherwise dataset)
#    - Name is the directory name, namespace is you
tf up ./imdb-ja
```

For an existing repository, `tf up` pushes only the diff as a single commit. Use `--dry-run`
to preview it beforehand, and `--delete` to also remove remote files that no longer exist
locally.

In places without interactive login — CI, scripts — set `THINKINGFACE_API_KEY` (and
`THINKINGFACE_ENDPOINT`) instead. That puts every command in the same state as having already
run `tf login`, without touching the config file:

```bash
export THINKINGFACE_ENDPOINT=http://localhost:8080
export THINKINGFACE_API_KEY=tf_xxxxxxxxxxxx   # a write-scoped token from /settings/tokens
tf status
tf up ./imdb-ja
```

## Command reference

Every subcommand except `version` accepts these flags (`version` takes only `--json`):

| Flag | Meaning |
|---|---|
| `--endpoint URL` | Server URL. If omitted, follows the [credential resolution order](#credential-resolution-order) |
| `--token TOKEN` / `--api-key KEY` | An access token; both flags set the same value. If omitted, follows the resolution order |
| `--verbose` | Print how the endpoint and token were resolved to stderr |
| `--json` | Machine-readable output: the result as JSON on stdout (one object on one line; `tf experiments sync --watch` prints one line per pass). Progress, warnings and errors stay on stderr and the exit codes are unchanged, so a script can parse stdout without filtering it. Accepted by every command except `tf mcp`, including `tf version` |

Every subcommand also accepts `-h` / `--help`, which prints usage to stdout and exits `0`
(distinct from a usage error, which prints to stderr and exits `2`).

**Exit codes**: `0` success, `1` failure (`tf: <message>` on stderr), `2` usage error.

### `tf login [ENDPOINT] [flags]`

Logs in to a server and saves a token to the config file.

```text
tf login [ENDPOINT] [--token TOKEN | --token -]
         [--username USER] [--password-stdin] [--name NAME]
```

| Flag | Meaning |
|---|---|
| `ENDPOINT` | The server URL. If omitted, follows the same [credential resolution order](#credential-resolution-order) as every other command (`TF_ENDPOINT` / `THINKINGFACE_ENDPOINT` / `HF_ENDPOINT`, then the config file's default endpoint) — `login`/`logout` are not a special case here; if none of those resolve anything and stdin is a terminal, `tf login` prompts for it instead of erroring |
| `--token TOKEN` | Verifies the given token with `whoami` and saves it as-is. `--token -` reads the token from stdin (one line) instead |
| `--username USER` | Username for password-based login (used when `--token` is not given) |
| `--password-stdin` | Reads the password from stdin (one line) instead of prompting with echo disabled |
| `--name NAME` | Name for the token minted during password login (default `tf-cli@<hostname>`) |

Without `--token`, `tf login` signs in with a username and password and mints a new
write-scoped personal access token — the password prompt has echo disabled on a terminal, or
is read from stdin via `--password-stdin` when piped. A warning is printed if the resulting
token's scope turns out to be `read` (`tf up` needs a write-scoped token).

With `--json`, `tf login` prints `{"endpoint", "username", "scope", "token_id", "minted",
"config_path"}` instead of its confirmation lines. There is deliberately no token field: the
output of a login is easily captured into a CI log. `token_id` is `0` and `minted` is `false` for a
token pasted in with `--token`.

Logging in again against an endpoint you already logged in to revokes the token that
previous `tf login` minted for it, once the new one is safely saved — `tf` prints a note to
stderr (`revoked the token saved by the previous tf login (id N)`) when this happens. A
token you pasted in with `--token` is never revoked this way; only `tf logout` (or you
yourself) takes it away.

### `tf logout [ENDPOINT]`

Forgets the saved credentials for a server (default: the configured default endpoint). If the
saved token was minted by `tf login` itself (as opposed to pasted in with `--token`), it is
also revoked server-side on a best-effort basis. `--json` prints `{"endpoint", "revoked"}`, plus
`"revoke_error"` when the best-effort revoke failed (the logout itself still succeeds).

### `tf whoami`

Shows the identity behind the current token: name, email, the token's scope, organization
memberships, and the namespaces you can push to (yourself plus any organization where you
hold `admin` or `write`). `--json` prints `{"name", "fullname", "email", "scope", "endpoint",
"orgs": [{"name", "role"}], "push_to"}`.

### `tf status [--json]`

A summary of where and as whom `tf` would currently connect: the resolved endpoint and token
(and where each came from), whether the server accepts the token, the identity behind it, the
namespaces you can push to, the config file location, and every saved login.

```text
$ tf status
endpoint:   http://localhost:8080 (from env THINKINGFACE_ENDPOINT)
token:      tf_…9f2a (from env THINKINGFACE_API_KEY)
logged in:  yes
user:       admin (Admin) <admin@example.com>
scope:      write
push to:    admin
config:     /home/admin/.config/thinkingface/config.json (no saved logins)
```

The token is shown masked (first 3, last 4 characters). Exit code is `0` when logged in and
`1` otherwise, so scripts can use `tf status` as a precondition check directly. `--json`
prints the same information to stdout as one JSON object (`logged_in`, `user`, `push_to`,
`saved_endpoints`, and so on) instead of the table above.

### `tf up PATH [flags]`

The core command. Pushes the contents of PATH (a file or directory) to a repository as a
single commit, creating the repository first if it does not exist.

```text
tf up PATH [--to NS/NAME|NAME] [--kind dataset|model] [--rev BRANCH]
           [-m/--message MSG] [--license L] [--tag T ...] [--desc TEXT]
           [--include GLOB ...] [--exclude GLOB ...] [--hidden]
           [--delete] [--dry-run]
           [--workers N] [--quiet] [--json]
```

| Flag | Default | Meaning |
|---|---|---|
| `--to NS/NAME` or `NAME` | your namespace + a name derived from PATH | Destination repository. A `datasets/` or `models/` prefix on `NS/NAME` also pins the kind. The derived name is PATH's directory name when PATH is a directory, or its file name **with the extension stripped** when PATH is a single file (`tf up ./model.safetensors` targets a repository named `model`, not `model.safetensors`) |
| `--kind dataset\|model` | inferred from contents | Pins the repository kind explicitly, overriding `--to`'s prefix |
| `--rev` | `main` | Branch to push to |
| `-m`, `--message` | `Upload N files with tf` (`Upload 1 file with tf` for exactly one; `Delete N files with tf` when the run only deletes, nothing is uploaded) | Commit summary |
| `--license` | (unset) | The repository card's `license` |
| `--tag` | (unset) | The repository card's `tags`. Repeatable; a single occurrence may also carry comma-separated values (`--tag a,b --tag c` → `a`, `b`, `c`) |
| `--desc` | (unset) | The repository card's `description`, also used as the opening paragraph of a generated README |
| `--include` | include everything | Only include files matching this glob (repeatable). Not a shell glob run through the shell — `tf` matches it itself, with `**` matching any number of path segments (`data/**`, `**/*.parquet`), `[...]` matching one character out of a set (`[ab].csv`, `[a-z]*`, `[!0-9]*` negated), and, for a pattern with no `/` at all, also tried against just the file's base name (`*.parquet` matches `data/train.parquet` too) |
| `--exclude` | (none) | Exclude files matching this glob (repeatable). Same matching rules as `--include` |
| `--hidden` | off | Also upload dot-files and dot-directories found under PATH. They are skipped by default (see below); `.gitattributes` and `.gitignore` are always uploaded either way |
| `--delete` | off | Delete remote files that are not present anywhere on disk under PATH, regardless of `--include`/`--exclude` — a file those flags kept out of this run's upload but that still exists on disk is never deleted |
| `--dry-run` | off | Show what would happen without changing anything |
| `--workers` | `4` | Number of parallel LFS transfers |
| `--quiet` | off | Suppress progress output on stderr |
| `--json` | off | Print the final result to stdout as one line of JSON (progress still goes to stderr unless combined with `--quiet`) |

**Kind determination order**: `--kind` beats the `datasets/`/`models/` prefix on `--to`, which
beats inference from the directory's contents (model if things like `*.safetensors` or
`config.json` are present, otherwise dataset).

**When the destination doesn't exist**: if neither `--kind` nor a `--to` prefix pins the
kind, and no repository is found under the inferred kind, `tf up` also checks for an existing
repository under the *other* kind before creating a new one (for example, inference says
dataset, but a model repository of the same name already exists — that one is used instead).

!!! warning "Dot-files are not uploaded by default"
    A repository here is readable by anyone who can reach the server, and a project
    directory usually holds more than the data: `.env`, `.envrc`, `.aws/credentials`,
    `.ssh/`, an editor's `.idea/`. So `tf up` leaves every dot-file and dot-directory it
    finds *inside* PATH out of the upload and prints one line on stderr naming what it
    skipped — a warning `--quiet` does not suppress. Two names are always uploaded, since
    they are repository content rather than machine state: `.gitattributes` (which carries
    the LFS routing rules) and `.gitignore`.

    Two things this rule does *not* cover. A path you name yourself is a choice you made,
    so `tf up ./.config` and `tf up ./.env` still upload it. And a dot-file that was
    uploaded by an earlier run stays on the remote: it is still on disk, so `--delete` does
    not read "not part of this upload" as "deleted locally" (see below). Remove such a file
    from the remote deliberately — from the Web UI, or with a `git push` — rather than
    expecting this flag to retract it.

    Pass `--hidden` to upload them anyway. `--include` does not override the rule on its
    own: a dot-file needs `--hidden` even when a pattern names it.

!!! warning "`--delete` protects two files"
    `--delete` never removes the root `.gitattributes` or `README.md`, even if they are
    absent locally. `.gitattributes` is server-generated and decides LFS routing for later
    uploads; `README.md` may hold a repository card generated from `--license`/`--tag`/`--desc`
    on a previous run.

!!! note "`--delete` and `--include`/`--exclude` together"
    A file kept out of the upload by `--include`/`--exclude` is still checked against disk,
    not against the upload set: as long as it exists somewhere under PATH, it survives
    `--delete`. Only files genuinely absent from PATH are removed. The same holds for a
    dot-file skipped by the rule above, and for everything under a skipped dot-directory.

!!! note "`--delete` and symlinked directories: blind spots are left alone"
    The local scan does not follow a symlink that points at a directory (following it could
    loop), a broken symlink, or a non-regular file (a socket, a fifo, ...) — `.git` and
    `__pycache__` directories are skipped the same way, silently. `tf up` prints a warning to
    stderr for every skip that isn't `.git`/`__pycache__` (up to 10, then a count of the
    rest), and **this warning is not suppressed by `--quiet`** — `--quiet` only suppresses
    progress output, and silently leaving content out of an upload isn't progress.

    A symlinked directory in particular is a blind spot for `--delete`: since the scan never
    read what's inside it, `tf` cannot tell whether a remote path under that directory still
    exists locally or not, so it leaves everything under it alone rather than guess. A remote
    file whose local counterpart is now behind a directory symlink is therefore never deleted,
    even with `--delete`, until the symlink is resolved into a real directory (or removed) on
    a later run.

**README handling**: `tf up` never leaves a local `README.md` out of the upload on its own — a
local `README.md` is a normal file like any other, uploaded (and so overwriting whatever is on
the remote) whenever it's part of the run. What's described below is only about whether its
*content* gets touched before that upload, and it works off the filtered file set (after
`--include`/`--exclude`), not off what's physically on disk:

- If none of `--license`, `--tag`, `--desc` is given, no README content is generated or
  merged — a local `README.md` uploads exactly as it is on disk (if it's in the filtered set).
- If any of those flags is given **and** `README.md` is in the filtered file set, only the
  given values are merged into its existing frontmatter (the body and key order are
  preserved).
- If any of those flags is given and `README.md` is **not** in the filtered file set — either
  because there truly is no local `README.md`, or because `--include`/`--exclude` excluded it
  — a brand new `README.md` is generated from the card flags and included in the upload,
  silently replacing whatever `README.md` exists on the remote. A local `README.md` that
  `--include`/`--exclude` filtered out of this run does not protect the remote file the way it
  would if the flags were simply left off.

The shape of `tf up --json`'s output:

```json
{
  "repo": "admin/imdb-reviews",
  "kind": "dataset",
  "rev": "main",
  "created": true,
  "commit": "abc1234def5678",
  "url": "http://localhost:8080/datasets/admin/imdb-reviews",
  "commit_url": "http://localhost:8080/datasets/admin/imdb-reviews/commit/abc1234def5678",
  "files": 3,
  "lfs_files": 2,
  "unchanged": 1,
  "deleted": 0,
  "bytes": 141557760,
  "uploaded_bytes": 129394688,
  "dry_run": false,
  "nothing_to_do": false
}
```

!!! note "About the printed URL"
    `url` is the endpoint's origin plus the web UI's path
    (`/datasets/{ns}/{name}` or `/models/{ns}/{name}`). If your API and web UI are on
    different origins (as in the docker compose development setup, `:8080` vs. `:3000`),
    swap in the web UI's origin and keep the path as-is.

### `tf experiments` { #tf-experiments }

Queries, waits on, annotates, imports and syncs experiment runs — the command-line side of
[Tracking Experiments](../guides/experiments.md#work-with-runs-from-the-command-line). `tf exp` is an
alias.

```text
tf experiments runs     REPO PROJECT [--group G] [--status S] [--tag T] [--archived true|false]
                        [--sort SPEC] [--order asc|desc] [--limit N] [--columns LIST]
tf experiments run      REPO PROJECT RUN
tf experiments wait     REPO PROJECT RUN [--until EXPR] [--timeout 24h] [--ignore-stale]
tf experiments diff     REPO PROJECT [RUN ...] [--include-meta]
tf experiments goals    REPO PROJECT [METRIC=min|max|none ...]
tf experiments notes    REPO PROJECT [--set FILE|-] [--base-sha SHA] [--force] [-m MSG]
tf experiments annotate REPO PROJECT RUN [--note TEXT | --note-file FILE|-] [--tag T ...]
                        [--clear-tags] [--add-tag T ...] [--remove-tag T ...] [--archive | --unarchive]
tf experiments import   REPO PROJECT FILE... [--configs FILE] [--status finished|failed]
                        [--replace] [--format csv|jsonl] [--dry-run]
tf experiments sync     [DIR ...] [--watch] [--interval 60s]
```

Every subcommand also takes `--json` and the common `--endpoint` / `--token` / `--api-key` /
`--verbose` flags; `tf experiments help SUBCOMMAND` prints its full usage.

- `REPO` is the experiment repository as `ns/name` (a `datasets/` prefix is accepted). `PROJECT`
  and `RUN` are passed through verbatim: names with `/`, spaces, `%` or non-ASCII characters work.
- Reads work without a token on an instance that allows anonymous reads. Writes — `goals` with
  arguments, `notes --set`, `annotate`, `import`, `sync` — need a write-scoped token for the
  repository.
- With `--json`, `runs`, `run`, `diff`, `goals`, `notes` and `annotate` print the API's response
  unchanged; `wait`, `import` and `sync` print the shapes described below.

#### `tf experiments runs`

A table of a project's runs: name, status, last step, last update, then extra columns. Filtering
and sorting happen on the server.

| Flag | Meaning |
|---|---|
| `--group G` | only runs of sweep group `G`. Repeatable: any of |
| `--status S` | only runs whose status is `running`, `finished`, `failed` or `stale`. Repeatable (or comma-separated): any of |
| `--tag T` | only runs carrying tag `T`. Repeatable: all of |
| `--archived true\|false` | only archived / only unarchived runs (default: both) |
| `--sort SPEC` | `name`, `started_at`, `updated_at`, `last_step`, `last:<metric>`, `min:<metric>`, `max:<metric>`, `best:<metric>` (needs a [goal](#tf-experiments-goals)), `config:<dotted.key>`. Runs lacking the value always sort last |
| `--order asc\|desc` | default `asc`; `best:` always puts the best run first |
| `--limit N` | at most `N` runs (1–1000) |
| `--columns LIST` | comma-separated extra columns: `config:<key>`, `last:<metric>` (alias `metric:<metric>`), `min:<metric>`, `max:<metric>`, `best:<metric>` (min or max by the goal), `group`, `job_type`, `tags`, `points`, `note` |

Without `--columns`, the table shows the group when any run has one, every metric with a goal in
its goal's direction (`min:loss`, `max:acc`), then the last value of up to three other metrics
(`_`-prefixed system metrics are skipped; a note on stderr says how many were left out). A `*`
marks the best non-archived run of each goal metric. `--json` prints `{"runs": [...],
"metric_goals": {...}, "best": {metric: run}}`.

#### `tf experiments run`

One run as a key/value block: status (with its heartbeat), step and point count, timestamps, group,
tags, note, produced models, the flattened config, and the last / min / max of every metric.
`--json` prints `{"run": {...}}`.

#### `tf experiments wait`

Blocks until a run satisfies `--until EXPR` (default `status!=running`), using the server's long
poll. The run does not have to exist yet: a 404 is retried every 5 seconds. Network errors, 5xx
and 429 are retried with backoff; 400 / 401 / 403 end the wait at once.

```text
expr    := and ( "or" and )*          "and" binds tighter than "or"
and     := primary ( "and" primary )*
primary := "(" expr ")" | cond
cond    := FIELD OP VALUE             OP: == != >= <= > <
FIELD   := step | points | status | metric:<m> | last:<m> | min:<m> | max:<m>
```

- `step` is the last logged step, `points` the number of points, `metric:` / `last:` a metric's
  last value, `min:` / `max:` its extremes so far.
- `status` compares with `==` / `!=` only, against `running`, `finished`, `failed` or `stale`;
  anything else is a parse error rather than a condition that is silently never true.
- A metric name ends at whitespace, a parenthesis, `= ! < >` or a quote, so `metric:val/CER<0.2`
  works bare; otherwise quote it right after the colon: `metric:"val loss" < 0.2`.
- Keywords and field names are case-insensitive. A comparison on a metric the run has not logged is
  false for every operator, `!=` included.

| Exit code | Meaning |
|---|---|
| `0` | the condition holds |
| `1` | `--timeout` passed (default `24h`; `0` waits forever), or the run stopped without meeting the condition |
| `2` | usage error, including an `--until` that does not parse (the message points at the column) |

"Stopped" means the run is no longer `running` — finished, failed or stale — while the condition
neither holds nor mentions `status`, so it cannot become true; the wait ends instead of hanging
until the timeout. `--ignore-stale` disables that check. The last state is printed on stdout in
every non-usage case; `--json` prints `{"run": {...}|null, "met": bool, "reason":
"met"|"timeout"|"stopped", "until": "EXPR"}` (`run` is `null` when it never appeared).

#### `tf experiments diff`

The config keys, flattened to dotted paths, whose value differs between the named runs — or
between every non-archived run (at most 200) when none is named — one column per run, `-` where a
run lacks the key. `--include-meta` also compares the `_meta` and `_resume` subtrees.

#### `tf experiments goals` { #tf-experiments-goals }

Without arguments, prints the project's metric goals. With `METRIC=min`, `METRIC=max` or
`METRIC=none` arguments, merges them in (`none` removes a goal; goals not mentioned are left
alone; the last `=` splits, so a metric name may contain `=`). Works before the project has any
run. Goals drive the best-run markers and `--sort best:<metric>`.

#### `tf experiments notes`

Prints the project's notes, `{project}/NOTES.md` on the default branch (nothing on stdout, and a
note on stderr, when there are none yet). `--set FILE` (or `-` for stdin) replaces them, with `-m`
as the commit message. `--set` reads the current version first and sends it as the base, so the
server refuses the write (exit 1) if someone saved in between; for a read-edit-write across two
invocations, read with `--json` and pass its `blob_sha` back with `--base-sha` (`--base-sha ""`
means "the notes must not exist yet"). `--force` overwrites unconditionally.

#### `tf experiments annotate`

Changes a run's note (`--note TEXT`, `--note-file FILE|-`; `--note ""` clears it), tags (`--tag T`
replaces the set, `--clear-tags`, `--add-tag T` / `--remove-tag T` edit it), or archived flag
(`--archive` / `--unarchive`). Only what a flag names changes; no flag at all is a usage error.

#### `tf experiments import`

Imports past runs from CSV (with a header row) or JSONL files into `PROJECT`, creating the
repository if it does not exist. Each row is one point: `run` and `step` (an integer) are
required, `timestamp` (RFC 3339 or unix seconds) is optional, every other column is a metric.
Only numeric values are imported; empty, non-numeric, `NaN` and infinite cells are skipped and
counted.

| Flag | Meaning |
|---|---|
| `--configs FILE` | JSONL of `{"run", "config", "status", "group", "job_type"}`: the config sent with the run, its final status and sweep grouping |
| `--status S` | final status of runs without a `--configs` status: `finished` (default) or `failed` |
| `--replace` | delete existing runs of the same name first. Without it, any run that already exists refuses the whole import before anything is sent |
| `--format csv\|jsonl` | parse every file as this format (default: by extension, `.csv` / `.jsonl` / `.ndjson`) |
| `--dry-run` | parse, validate and check for existing runs; send nothing |

`--json` prints `{"runs": [{"run", "points", "status", "replaced"}], "skipped_cells": N}` (plus
`"dry_run": true` on a dry run).

#### `tf experiments sync`

Uploads runs `thinkingface.trackio` recorded to disk — in offline mode, or points the online mode
could not deliver (see [Offline runs](../guides/experiments.md#offline-runs-and-tf-experiments-sync)).
Each `DIR` is either the parent directory holding one subdirectory per run, or one run directory
(it contains `run.jsonl`); the default is `./thinkingface-offline`.

Progress is kept in each run directory's `sync-state.json`, so a sync can be interrupted and
re-run. A run with no finish record yet is synced up to its current end and left open; a finished
one gets its artifacts committed, its status set and its produced models recorded, and is skipped
afterwards. `--watch` repeats the pass every `--interval` (default `60s`) until interrupted. An
error in one directory does not stop the others; the exit code is `1` if any failed. `--json`
prints one object per pass:

```json
{"runs": [{"dir": "thinkingface-offline/20260927T101500-ocr-lr_0.01-1a2b3c4d", "repo": "alice/trackio-metrics",
  "project": "ocr", "run": "lr-0.01", "synced_lines": 812, "points": 8100, "done": true, "status": "finished",
  "artifacts_uploaded": 2, "artifacts_skipped": 0}]}
```

### `tf mcp` { #tf-mcp }

```text
tf mcp [--endpoint URL] [--token TOKEN] [--verbose]
```

Serves the `tf experiments` functionality to an AI agent over the
[Model Context Protocol](https://modelcontextprotocol.io/) on stdin/stdout. The agent's client
starts it as a subprocess; there is no port and no daemon. It uses the same credentials as every
other command, resolved on the first tool call rather than at startup, so a server started before
`tf login` keeps running and answers each call with an error naming the remedy until credentials
exist. `--verbose` logs credential resolution and each request to stderr; without it, `tf mcp`
writes nothing there.

See [Using thinkingface from AI agents](../guides/agents.md) for registering it with a client and
for the tools it offers.

### `tf version`

Prints `tf <version> (<GOOS>/<GOARCH>)`. `--json` prints `{"version", "os", "arch",
"go_version"}`.

### `tf help [COMMAND]`

Prints general usage, or detailed usage for one command (equivalent to
`tf COMMAND --help`).

## Credential resolution order

For every command, `tf` decides the endpoint and token with the following precedence.

**Endpoint**: `--endpoint` flag > `TF_ENDPOINT` > `THINKINGFACE_ENDPOINT` > `HF_ENDPOINT` >
the config file's default endpoint. An error is raised if none of these is set.

**Token**: `--token` / `--api-key` flag > `THINKINGFACE_API_KEY` > `TF_TOKEN` >
`THINKINGFACE_TOKEN` > a token saved in the config file for the resolved endpoint >
`HF_TOKEN` (only when `HF_ENDPOINT`, normalized, equals the resolved endpoint — a safeguard
against accidentally sending a token meant for the real huggingface.co to a thinkingface
server) > unset (anonymous; `tf up` and `tf whoami` refuse to run anonymous).

Setting only `THINKINGFACE_API_KEY` and `THINKINGFACE_ENDPOINT` is therefore enough to make
every command behave as though `tf login` had already run, without ever writing the config
file. Pass `--verbose` to any command to see which source resolved each value
(`from flag`, `from env TF_ENDPOINT`, `from config`, and so on) on stderr.

## Config file

The save location is `$TF_CONFIG` if set, otherwise `$XDG_CONFIG_HOME/thinkingface/config.json`,
defaulting to `~/.config/thinkingface/config.json`. Permissions are `0600` for the file and
`0700` for the directory; writes go through an atomic rename via a temporary file.

One token is saved per endpoint (keyed by the normalized endpoint URL), and the most
recently logged-in endpoint becomes the default used when a command is run without
`--endpoint` or an endpoint environment variable. The file looks like:

```json
{
  "default_endpoint": "http://localhost:8080",
  "credentials": {
    "http://localhost:8080": {
      "endpoint": "http://localhost:8080",
      "token": "tf_xxxxxxxxxxxx",
      "token_id": 42,
      "username": "admin",
      "created_at": "2026-08-23T09:00:00Z"
    }
  }
}
```

`token_id` is `0` when the saved token was pasted in with `--token` rather than minted by
`tf login` — both `tf logout` and a later `tf login` against the same endpoint use it to
decide whether there's a previously minted token to revoke server-side.

## Relationship to `hf upload`

Because `tf` speaks exactly the HF-compatible API the thinkingface server already exposes,
anything `tf up` can do (aside from kind inference and repository-card generation) can also
be done with `huggingface_hub`'s `hf upload` / `HfApi`:

```bash
export HF_ENDPOINT=http://localhost:8080
export HF_TOKEN=tf_xxxxxxxxxxxx
export HF_HUB_DISABLE_XET=1
hf upload admin/imdb-reviews ./imdb-ja . --repo-type dataset
```

`tf` simply wraps that procedure and removes the need to manage `HF_ENDPOINT`/`HF_TOKEN`,
choose `--repo-type`, and pre-create the repository — it introduces no compatibility
difference of its own. See [Compatibility](compatibility.md) for what is and isn't verified
to work through `huggingface_hub`.

## Known limitations

- `tf up gs://...` is not supported (it produces a `gs:// import is not supported yet`
  error). For data that lives in GCS, copy it locally first and then run `tf up`.
- The command name `tf` can collide with Terraform (in environments that alias `terraform`
  to `tf`) or with TensorFlow's own tooling. Watch for shell alias configuration if `tf`
  doesn't behave as documented here.

## See also

- [Uploading Files](../guides/uploading.md) — a task-oriented walkthrough of getting data in
- [Authentication](authentication.md) — how access tokens are issued and scoped
- [Tracking Experiments](../guides/experiments.md) — what the `tf experiments` commands operate on
- [Using thinkingface from AI agents](../guides/agents.md) — `tf mcp` and the agent workflow
- [Compatibility](compatibility.md) — what's verified to work through `huggingface_hub` and git
