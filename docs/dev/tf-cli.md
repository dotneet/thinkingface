# `tf` CLI

`tf` is a single static-binary command-line client for registering datasets/models with
thinkingface. The goal is to boil registration down to two commands:

```bash
tf login https://tf.example.com   # once only
tf up ./imdb-ja                   # everything else is just this
```

In places without interactive login — CI, scripts — setting the environment variable
`THINKINGFACE_API_KEY` (plus `THINKINGFACE_ENDPOINT`) puts you in the same state as having
already run `tf login`:

```bash
export THINKINGFACE_ENDPOINT=https://tf.example.com
export THINKINGFACE_API_KEY=tf_xxxxxxxxxxxx   # a write token issued from the Web UI's /settings/tokens
tf status                                     # check login state and your own info
tf up ./imdb-ja
```

## Motivation

Registering even a single dataset/model involves a long plain-vanilla procedure: open the
site in a browser → add a repository → `git clone` locally → put files in place → commit
→ push. Using `huggingface_hub`'s `hf upload` shortens the steps, but there's still the
friction of setting `HF_ENDPOINT` / `HF_TOKEN`, forgetting `--repo-type dataset`, and
having to think "what's the namespace? what's the name?" every single time.

`tf` eliminates this. It has no protocol of its own — it's a thin client against the
HF-compatible API the server already exposes (whoami / create_repo / preupload / LFS
batch / commit; `docs/dev/api-contract.md` §1–§3), so anything `hf upload` can do, `tf up` can
do too. The differences are:

- If the repository doesn't exist, `tf up` creates it first (it doesn't rely on the
  server's auto-create)
- The kind (dataset/model) is inferred from the directory's contents
- The name is the directory name, and the namespace is you (overridable with `--to`)
- Save credentials once with `tf login`, and subsequent commands don't need to remember
  the token

## Installation

If you have Go 1.25 or later:

```bash
go install github.com/dotneet/thinkingface/backend/cmd/tf@latest
```

If you've already cloned this repository, you can also build it locally:

```bash
make tf   # builds to backend/bin/tf
```

`make tf` embeds a version string via
`-ldflags "-X .../tfcli.Version=$(git describe --tags --always --dirty)"`
(check it with `tf version`).

## Quick start

```bash
# 1. Log in to the server (issues and saves a single token)
tf login https://tf.example.com

# 2. Register a directory as-is
#    - Kind is inferred from file contents (model if safetensors/config.json exist, otherwise dataset)
#    - Name is the directory name (imdb-ja), namespace is you
tf up ./imdb-ja
```

For an existing repository, `tf up` pushes only the diff as a single commit
(`--dry-run` for a preview beforehand, `--delete` to also remove files from the remote
that no longer exist locally).

## Command reference

Flags common to every subcommand:

| Flag | Meaning |
|---|---|
| `--endpoint URL` | Server URL (if omitted, follows the "credential resolution order" below) |
| `--token TOKEN` / `--api-key KEY` | API token (same value either way; omission behaves the same as above. The `login` subcommand alone gives this a different meaning — see below) |
| `--verbose` | Print how the credentials were resolved (where endpoint/token came from) to stderr |
| `--json` | Machine-readable output: one JSON object on one line of stdout (the shapes are listed per command). Progress, warnings and errors stay on stderr and the exit codes are unchanged, so a script can parse stdout without filtering it. Without `--json`, the human output is exactly what it was before the flag existed |

Exit codes: `0` success / `1` failure (`tf: <message>` on `stderr`) / `2` usage error
(prints usage to `stderr`; `tf help` / `--help` / `-h` themselves print usage to `stdout`
and exit `0`).

### `tf login [ENDPOINT] [flags]`

Logs in to the server and saves the token to the config file.

```
tf login [ENDPOINT]
         [--token TOKEN | --token -]
         [--username USER] [--password-stdin] [--name NAME]
```

- If `ENDPOINT` is omitted, `login` resolves it the same way every other command does
  (`--endpoint` positional aside) — `TF_ENDPOINT` > `THINKINGFACE_ENDPOINT` > `HF_ENDPOINT` >
  the config file's default endpoint (`resolveEndpoint` in
  `backend/internal/tfcli/login.go`) — and only falls back to an interactive prompt, when
  stdin is a terminal, if none of those resolve anything.
- Passing `--token` verifies that token via `whoami` and saves it as-is
  (`--token -` reads one line from stdin as the token).
- Without `--token`, it logs in with username/password and issues and saves a new
  write-scoped personal access token. The password is entered with hidden input on a
  terminal, or, when piped, passed as one line on stdin via `--password-stdin`.
- A warning is shown if the issued token's scope turns out to be `read` (`tf up` needs
  write scope).
- Logging in again against an endpoint that already has a saved credential revokes the
  token the previous `tf login` minted for it (`backend/internal/tfcli/login.go` ~215-254),
  once the new credential is safely saved to disk — mirrors `tf logout`'s own revoke rule.
  The credential to revoke is captured (`prevCred`) before `file.Set` overwrites it; a
  credential with `TokenID == 0` (pasted in via `--token`, never minted by `tf login`) is
  never revoked here, and neither is a previous credential whose token is byte-for-byte the
  new one being saved (covers `tf login --token T` where `T` is exactly what an earlier
  login minted). On success, a note goes to stderr:
  `revoked the token saved by the previous tf login (id N)`; on failure, a
  `could not revoke previous token` warning, non-fatal either way.

- `--json` prints `{"endpoint", "username", "scope", "token_id", "minted", "config_path"}`
  instead of the two confirmation lines. There is deliberately no token field — stdout of a
  login is easily captured into a CI log. `token_id` is `0` and `minted` `false` for a
  pasted `--token`.

### `tf logout [ENDPOINT]`

Deletes saved credentials. A token that `tf login` itself issued (as opposed to one
pasted in via `--token`) — i.e. one issued via username/password — is also revoked
server-side on a best-effort basis. `--json` prints `{"endpoint", "revoked",
"revoke_error"}` (`revoke_error` only when the best-effort revoke failed; the logout itself
still succeeds).

### `tf status`

A summary of where and as whom `tf` is currently connecting. Shows the resolved endpoint
/ token and where each came from (flag / env / config), whether the server accepts the
token, the token owner's identity (name, email, scope, org memberships), the namespaces
you can push to, the config file's location, and the list of saved logins. The token is
shown as only its first 3 and last 4 characters.

```
$ tf status
endpoint:   https://tf.example.com (from env THINKINGFACE_ENDPOINT)
token:      tf_…9f2a (from env THINKINGFACE_API_KEY)
logged in:  yes
user:       alice (Alice) <alice@example.com>
scope:      write
orgs:       team (admin)
push to:    alice, team
config:     /home/alice/.config/thinkingface/config.json (no saved logins)
```

The exit code is `0` when logged in and `1` when not (no token / rejected by the server /
no endpoint configured), so it can be used directly as a precondition check in scripts.
`--json` prints the same content to stdout as a single JSON object (`logged_in` / `user` /
`push_to` / `saved_endpoints`, etc.).

### `tf whoami`

Shows the owner of the current token: name, email, the token's scope, org memberships,
and the list of namespaces you can push to (yourself plus any org where you hold
`admin`/`write` permission). `--json` prints `{"name", "fullname", "email", "scope",
"endpoint", "orgs": [{"name", "role"}], "push_to"}`.

### `tf up PATH [flags]`

The core command. Pushes the contents of PATH (a file or directory) to a repository as a
single commit, creating the repository first if it doesn't exist.

```
tf up PATH [--to NS/NAME|NAME] [--kind dataset|model] [--rev BRANCH]
           [-m/--message MSG] [--license L] [--tag T ...] [--desc TEXT]
           [--include GLOB ...] [--exclude GLOB ...] [--hidden]
           [--delete] [--dry-run]
           [--workers N] [--quiet] [--json]
```

| Flag | Default | Meaning |
|---|---|---|
| `--to NS/NAME` or `NAME` | your namespace + a name derived from PATH | The destination repository. Adding a `datasets/` or `models/` prefix also pins the kind. The derived name is PATH's directory name when PATH is a directory, or its file name **with the extension stripped** when PATH is a single file (`RepoNameFromPath` in `backend/internal/tfcli/local/local.go`) |
| `--kind dataset\|model` | inferred from contents | Explicitly pins the kind (takes priority over the `--to` prefix) |
| `--rev` | `main` | Branch to push to |
| `-m`, `--message` | `Upload N files with tf` (`Upload 1 file with tf` for exactly one file; `Delete N files with tf` when the run only deletes and uploads nothing — `commitSummary` in `backend/internal/tfcli/hub/upload.go`) | Commit summary |
| `--license` | (unset) | The repository card's `license` |
| `--tag` | (unset) | The repository card's `tags` (repeatable; comma-separated values can also be given together: `--tag a,b --tag c`) |
| `--desc` | (unset) | The repository card's `description` (also becomes the opening paragraph of the body in a generated README) |
| `--include` / `--exclude` | include everything | Narrows the file set (repeatable). These are **not** shell globs expanded by the shell — `tf` matches each pattern itself (`Match` in `backend/internal/tfcli/local/local.go`), with `**` matching any number of path segments (`data/**`, `**/*.parquet`), `[...]` bracket expressions matching one character (`[ab].csv`, `[a-z]*`, `[!0-9]*`/`[^0-9]*` negated), and a pattern with no `/` also tried against just the file's base name |
| `--hidden` | off | Also uploads dot-files and dot-directories found *inside* PATH. Without it they are skipped (see "Hidden paths" below), except the names in `alwaysKeptDotfiles` (`.gitattributes`, `.gitignore`) |
| `--delete` | off | Deletes remote files that don't exist anywhere on disk under PATH (excludes `.gitattributes` and `README.md` at the repository root — the former is server-generated LFS rules, and the latter may be a card generated from `--license` etc., so neither is removed just because it's absent locally). Independent of `--include`/`--exclude`: a file those flags kept out of this run's upload but that is still on disk is never deleted |
| `--dry-run` | off | Only shows what would happen; changes nothing |
| `--workers` | 4 | Number of parallel LFS transfers |
| `--quiet` | off | Suppresses progress output (stderr) |
| `--json` | off | Prints the final result to stdout as one line of JSON (progress still goes to stderr unless combined with `--quiet`) |

**Kind-determination order**: `--kind` > the `datasets/`/`models/` prefix on `--to`
> inference from the directory's contents (model if things like safetensors/config.json
are present, otherwise dataset).

**Behavior when the destination doesn't exist**: when neither `--kind` nor a `--to`
prefix is given, and no repository is found under the inferred kind, it also checks, just
in case, for an existing repository under the other kind (e.g. inference says dataset, but
a model repository of the same name already happens to exist — that one is used instead).
If neither exists, a new repository is created under the inferred kind.

**README handling**: this only concerns the README's *content*, and works off the filtered
file set (after `--include`/`--exclude`), not off what physically exists on disk
(`buildUploadFiles` in `backend/internal/tfcli/hub/upload.go`). If none of
`--license`/`--tag`/`--desc` is given, no README content is generated or merged — a local
`README.md` uploads exactly as it is on disk, if it's part of this run's filtered set. If any
of those flags is given and `README.md` is in the filtered set, only the given values are
merged into its existing frontmatter (body and key ordering are preserved). If any of those
flags is given and `README.md` is **not** in the filtered set — either because there is truly
no local `README.md`, or `--include`/`--exclude` excluded it — a brand-new `README.md` is
generated from the card flags and included in the upload, silently replacing whatever
`README.md` exists on the remote.

**Hidden paths**: repositories on this server are world-readable, and a project directory
routinely holds `.env`, `.envrc`, `.aws/credentials` or an editor's `.idea/` right next to
the data. So the scan leaves every dot-file and dot-directory it finds *inside* the tree out
of the upload (`isHidden` / `hiddenSkip` in `backend/internal/tfcli/local/local.go`), and
`tf up` prints one grouped line on stderr naming what was skipped and the `--hidden` flag
that includes them (`warnHidden` in `backend/internal/tfcli/up.go`; like every other skip
warning it survives `--quiet`). Two exceptions, in `alwaysKeptDotfiles`: `.gitattributes`
(the LFS routing rules — also in `protectedPaths`, so `--delete` never removes it either)
and `.gitignore`. The rule is about what the *walk* finds, not about the path the user
typed: `tf up ./.config` and `tf up ./.env` are explicit choices and still upload. Note
that `--include` does not override it -- the hidden check runs before the include/exclude
filters, so `--include .env` still needs `--hidden`.

The `--delete` side of this is the part to be careful about. A skipped dot-*file* is still
listed in `Scan`'s `allPaths`, so it reaches `hub.Plan.LocalPaths` and a remote copy of it
counts as "present on disk", not "gone locally" — a repository uploaded before this rule
existed keeps its `.env` on the remote rather than having it silently deleted by the next
`tf up --delete`. A skipped dot-*directory* is recorded as a `Skipped{Dir: true}` entry, so
it lands in `LocalUnknownDirs` and everything the remote holds beneath it is left alone for
the same reason a symlinked directory is (next paragraph).

**`--delete` and symlinked directories**: the local scan does not follow a symlink that
points at a directory (to avoid loops), a broken symlink, or a non-regular file (a socket, a
fifo, ...) — `.git` and `__pycache__` directories are skipped the same way, silently. `tf up`
prints a warning to stderr for every skip that isn't `.git`/`__pycache__` (dot-paths
excepted — those are grouped onto the single line described above), up to
`maxSkipWarnings = 10` (then a count of the rest) — see `warnSkipped` in
`backend/internal/tfcli/up.go`. **This warning is not suppressed by `--quiet`**: `--quiet`
only suppresses progress output, and silently leaving content out of an upload is not
progress. A symlinked directory in particular is a blind spot for `--delete`: since the scan
never read what's inside it, `tf` cannot tell whether a remote path under that directory
still exists locally, so it leaves everything under it alone rather than guess — a remote
file whose local counterpart is now behind a directory symlink is never deleted, even with
`--delete`, until the symlink is resolved into a real directory (or removed) on a later run.

The shape of the JSON that `tf up --json` prints:

```json
{
  "repo": "alice/imdb-ja",
  "kind": "dataset",
  "rev": "main",
  "created": true,
  "commit": "abc1234def5678",
  "url": "https://tf.example.com/datasets/alice/imdb-ja",
  "commit_url": "https://tf.example.com/datasets/alice/imdb-ja/commit/abc1234def5678",
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

### `tf experiments import REPO PROJECT FILE... [flags]`

Imports past runs into `PROJECT` of the dataset repository `REPO` (`ns/name`, optionally
prefixed `datasets/`; created if missing) through the same `POST .../log` and
`POST .../finish` endpoints the Python shim uses (`docs/dev/agent-features.md` §4, P8).

- **Input.** Each `FILE` is CSV (header row) or JSONL (one object per line; blank lines
  ignored), chosen by extension (`.csv` / `.jsonl` / `.ndjson`) or `--format csv|jsonl`.
  Several files may be given; rows of the same run are merged across them.
- **Columns / keys.** `run` (required), `step` (required, integer; `10.0` is accepted),
  optional `timestamp` (RFC 3339 or unix seconds, sent as RFC 3339 UTC); these three are
  matched case-insensitively. Everything else is a metric. Only numeric values are imported
  (JSONL numeric strings count); empty, non-numeric, `null`, boolean, nested, NaN and ±Inf
  cells are skipped and counted (`skipped_cells`). A row left with no metric is dropped.
  An unnamed column (pandas' index) and the names the server reserves for the metrics
  parquet's own columns (`_step`, `run_name`, `id`, ...) are ignored with a warning; a
  metric name the server would refuse (empty, over 256 bytes, control characters) fails
  the command.
- **Validation before sending.** Run / project / group names are checked like the server
  checks them, and a run with more than 1000 distinct metrics fails the whole import before
  any request.
- **Per run.** Points are sorted by step (stable) and sent in `/log` batches of at most
  10 000 points (and about 8 MiB of JSON), the first batch carrying the config; then
  `/finish` with the run's status.
- **`--configs FILE`**: JSONL of `{"run", "config", "status", "group", "job_type"}`
  (unknown keys ignored, a run listed twice is an error, an entry for a run with no rows
  is ignored with a warning). A run without an entry, or without a `status`, is finished
  with `--status` (`finished` by default, or `failed`).
- **Existing runs.** The project's run names are read first. Without `--replace`, any run
  that already exists refuses the **whole** import before anything is sent (the message
  lists them) — importing twice would duplicate points. `--replace` deletes each such run
  (`DELETE .../runs/{run}`) right before re-importing it.
- `--dry-run` parses, validates and checks for existing runs, and sends nothing (it does
  not create the repository either).
- Progress goes to stderr; stdout gets a summary, or with `--json`:

```json
{"runs": [{"run": "lr-0.01", "points": 25000, "status": "finished", "replaced": false}], "skipped_cells": 3}
```

(`"dry_run": true` is added on a dry run.) A failure part-way stops at the failing run and
names the runs already imported on stderr.

### `tf experiments sync [DIR ...] [flags]`

Replays runs the Python shim recorded to disk (`THINKINGFACE_MODE=offline`, or points the
online mode spilled instead of dropping) — the on-disk format and the sync-state contract
are in `docs/dev/agent-features.md` §2.9.

- `DIR` defaults to `./thinkingface-offline` (a missing default directory is simply
  empty). An argument containing `run.jsonl` is one run directory; any other directory is
  treated as the parent and each subdirectory containing `run.jsonl` is synced, in name
  order.
- **Reading `run.jsonl`.** Only complete lines are read: an unterminated last line (still
  being written) is left for a later pass, and a line that is not valid JSON (the writer
  died mid-line) ends the file (with a warning when more follows). A complete line of valid
  JSON that does not fit the record shape (`"step": true`, a bare `42`, ...) is skipped with
  a warning and the cursor moves past it, so one bad record cannot wedge the directory; the
  warning is only repeated for lines the cursor has not passed yet. Unknown fields are
  ignored; an unknown record type is skipped with a warning; a second `init` is ignored;
  lines after `finish` are ignored.
- **First sync** (`sync-state.json` has no `run` yet): `repo: null` resolves to
  `{whoami}/trackio-metrics`; the repository is created when missing. The run name follows
  the shim's resume rules against the project's current runs: `never` (or empty / unknown,
  with a warning) and a taken name → `name-1`, `name-2`, ...; `allow` continues the run;
  `must` fails the directory when the run does not exist. The decision (`run`, `repo`, and
  for a continued run the config the server held, as `base_config`) is written to
  `sync-state.json` with `synced_lines: 0` **before** anything is sent, so a crash after
  the first `/log` can never make the retry pick `name-1`, while line 0 — the `init`
  record — stays undelivered until a `/log` carrying its config succeeds (a failed first
  call re-sends it on the next pass).
- **Replay.** Records after `synced_lines` are replayed; consecutive `log` records are
  coalesced into `/log` calls of at most 10 000 points (a single record larger than that is
  split, and the cursor only passes it with its last piece). A call carries the most recent
  config seen in its records (the init config on the first call); a `null` or absent
  `config` in an `init` / `log` record sends none (the shim leaves it out of a spill once
  the online mode delivered it). When an existing run is continued (`resume` `allow` /
  `must`), **every** config sent is `base_config` with the record's config over it (stored
  keys kept, record keys win), in this pass and every later one. The calls carry
  `status: "running"` — unless the file already holds its `finish` record, in which case
  they carry that final status, so replaying a spill of a run whose online `/finish`
  succeeded never moves it back to `running` (no second `run.finished` webhook, no
  running/stale window). `null` / non-numeric metric values are dropped; metric names the
  server would refuse are dropped with a warning rather than blocking the directory
  forever. `synced_lines` is persisted
  (temp file + rename) after every successful call. A failed call stops that directory;
  the next pass re-sends from the last persisted line (at-least-once).
- **Finish.** At the `finish` record: the artifacts named by every `artifact` record are
  committed in one commit under `{project}/artifacts/{run}/{name}` (preupload / LFS /
  commit, like `tf up`). A path is skipped only when the server already has the same
  **content** — the git blob sha1 (or LFS sha256) of the local file equals the tree
  entry's, the same check `tf up` uses — never on size alone, since checkpoints of one
  model are routinely the same size; this is also what keeps a retry after a crash between
  the commit and the state write from committing twice. Missing files and paths outside
  the run directory are skipped with warnings. Then `/finish` with the recorded
  status (`finished` when empty or unknown); then, if there were `model` records, the
  produced-model list is `PATCH`ed onto the run — after `/finish`, because that is what
  creates a run that logged no points. Finally `done: true`. A done directory is skipped.
- **No finish yet.** The directory is synced up to its end and left not-done; a config-only
  call is made when there is a config but no points (likewise before `/finish` for a run
  that logged nothing, so its init config is not lost).
- **Errors.** A 403 names the repository the run resolved to (with the server's reason);
  for a run recorded with `repo: null` — which a token restricted to other repositories
  cannot write as `{you}/trackio-metrics` — it adds that `THINKINGFACE_REPO` chooses the
  repository when recording.
- `--watch` repeats the pass every `--interval` (default `60s`) until SIGINT/SIGTERM,
  picking up new run directories and new lines; each pass after the first stays quiet about
  directories that were already done. An interrupted pass loses nothing (the state only
  ever records acknowledged calls).
- Errors in one directory do not stop the others; the exit code is `1` if any directory
  failed (in `--watch`, if the last completed pass had a failure).
- `--json` prints one object per pass (one line each under `--watch`):

```json
{"runs": [{"dir": "thinkingface-offline/20260927T101500-ocr-lr_0.01-1a2b3c4d", "repo": "alice/trackio-metrics",
  "project": "ocr", "run": "lr-0.01", "synced_lines": 812, "points": 8100, "done": true, "status": "finished",
  "artifacts_uploaded": 2, "artifacts_skipped": 0}]}
```

(`already_done` marks a directory skipped because it was done before the pass;
`artifacts_skipped` counts artifacts whose content was already on the server; `error`
carries a failure.)

### `tf version`

Prints `tf <version> (<GOOS>/<GOARCH>)`. `--json` prints `{"version", "os", "arch",
"go_version"}`.

### `tf experiments` — querying, waiting on and annotating runs

The read/annotate half of the experiment tracker, for a person at a terminal and, above
all, for an AI agent driving training runs from a shell (the design contract is
`docs/dev/agent-features.md` §2 and §4). `tf exp` is an alias. `REPO` is always `ns/name`
(a `datasets/` prefix is accepted); `PROJECT` and `RUN` are passed through verbatim — names
with `/`, spaces, `%` or non-ASCII characters work, because every URL segment is escaped on
its own (`hub/experiments.go`).

All of these talk to `/api/v1/experiments/...`. Reads work without a token on a public
repository; writes (`goals` with arguments, `notes --set`, `annotate`) need a write token.
With `--json` each prints the API response unchanged (`apitypes` wire types), so its shape
is the one in `api-contract.md` §7; `wait` prints the envelope described below.
`tf experiments help <subcommand>` prints each one's full flag list.

| Command | Does |
|---|---|
| `runs REPO PROJECT` | Table of runs: name, status, step, last update, then extra columns. Filters/sort are server-side: `--group G` / `--status S` (repeatable: any of), `--tag T` (repeatable: all of), `--archived true\|false`, `--sort SPEC` (`name`, `started_at`, `updated_at`, `last_step`, `last:<m>`, `min:<m>`, `max:<m>`, `best:<m>`, `config:<dotted.key>`), `--order asc\|desc`, `--limit N` |
| `run REPO PROJECT RUN` | One run as a key/value block: status (+ heartbeat), step and point count, timestamps, group, tags, note, models, the flattened config, and last/min/max of every metric |
| `wait REPO PROJECT RUN` | Block until `--until EXPR` holds (below) |
| `diff REPO PROJECT [RUN ...]` | Config keys that differ between the runs (all non-archived runs when none are named), one column per run, `-` where a run lacks the key. `--include-meta` also compares `_meta` / `_resume` |
| `goals REPO PROJECT [METRIC=min\|max\|none ...]` | Without arguments, print the metric goals; with, merge them (`none` removes one; the last `=` splits, so a metric name may contain `=`). Works before the project has any run |
| `notes REPO PROJECT` | Print `{project}/NOTES.md` (nothing on stdout, a note on stderr, when it does not exist). `--set FILE\|-` replaces it (below) |
| `annotate REPO PROJECT RUN` | `--note TEXT` / `--note-file FILE\|-`, `--tag T` (repeatable; replaces the set) / `--clear-tags`, `--add-tag T` / `--remove-tag T` (read the current tags, then write the edited set), `--archive` / `--unarchive`. Only what a flag names changes; no flag at all is a usage error |

**Columns of `runs`.** `--columns` takes a comma-separated list of `config:<key>` (a literal
key first, then a dotted path into nested config), `last:<m>` (alias `metric:<m>`),
`min:<m>`, `max:<m>`, `best:<m>` (min or max by the metric's goal), `group`, `job_type`,
`tags`, `points`, `note`. Without it: `group` when any run has one, every metric with a goal
in its goal's direction (`min:loss`, `max:acc`), then `last:` of up to three other metrics
(alphabetically, `_`-prefixed system metrics skipped; a note on stderr says how many were
left out). A `*` marks the best non-archived run of each goal metric (the response's
`best` map), with a legend line under the table.

**Notes and concurrent edits.** `notes --set` never blindly overwrites: it reads the
current `blob_sha` and sends it as `base_sha`, so the server answers 409 (`tf` exits 1 and
says the notes changed) if someone saved in between. That only protects the moment between
the read and the write inside one invocation; an agent doing read → edit → write across
invocations reads with `notes --json` and passes that `blob_sha` back with `--base-sha`
(`--base-sha ""` means "the file must not exist yet"). `--force` overwrites unconditionally.
`-m` sets the commit message.

#### `tf experiments wait`

```
tf experiments wait REPO PROJECT RUN [--until EXPR] [--timeout 24h] [--ignore-stale] [--json]
```

Waits using the long-poll form of `GET .../runs/{run}` (`agent-features.md` §2.4): the
first request fetches the current state, each later one asks the server to hold for up to
55 s until the run's `updated_at` passes the last one seen or its derived status changes.
`since` is sent with nanosecond precision — a value truncated to the second is older than
the `updated_at` it came from and would make every poll return at once. Between polls that
brought no change, at least 2 s pass, so a server that ignores `wait` is not hammered.

- **The run may not exist yet.** A 404 is retried every 5 s (said once on stderr), so an
  agent can start waiting before the training job has logged anything.
- **Transient failures** (network errors, 5xx, 429) are retried with exponential backoff
  (1 s → 30 s). 400/401/403 end the wait at once with exit 1.
- **Exit codes:** `0` the condition holds; `1` timeout, or the run stopped (below); `2`
  usage, including an `--until` that does not parse (the error points at the column with a
  caret). The last state is printed on stdout in every non-usage case — the run block, or
  with `--json` `{"run": <ExpRun>|null, "met": bool, "reason": "met"|"timeout"|"stopped",
  "until": "<EXPR>"}` (`run` is `null` when it never appeared).
- `--timeout` defaults to 24 h; `0` waits forever.
- **A run that stops ends the wait.** When the run is no longer `running` (finished, failed
  or stale) and the condition neither holds nor mentions `status`, it cannot become true,
  so the wait exits 1 with `reason: "stopped"` instead of hanging until the timeout — an
  agent waiting for `step>=12000` on a job that crashed at step 5000 finds out as soon as
  the run goes stale. A condition that mentions `status` is left alone (the user asked about
  the status explicitly), and `--ignore-stale` disables the check (a stale run can come
  back; a finished one can be resumed).

`--until` grammar (`experiments_until.go`, a small recursive-descent parser):

```
expr    := and ( "or" and )*          "and" binds tighter than "or"
and     := primary ( "and" primary )*
primary := "(" expr ")" | cond
cond    := FIELD OP VALUE             OP: == != >= <= > <
FIELD   := step | points | status | metric:<m> | last:<m> | min:<m> | max:<m>
```

- `step` is `last_step`, `points` is `num_points`, `metric:`/`last:` the last value
  (`summary`), `min:`/`max:` the run's extremes (`summary_min` / `summary_max`).
- `status` compares only with `==`/`!=`, against one of `running`, `finished`, `failed`,
  `stale`; anything else (a typo such as `runnning`) is a parse error rather than a
  condition that is silently never true.
- A metric name ends at whitespace, a parenthesis, `= ! < >` or a quote, so
  `metric:val/CER<0.2` works bare; otherwise quote it right after the colon:
  `metric:"val loss" < 0.2` (`'...'` too; `\"` and `\\` escape inside double quotes).
- Keywords and field names are case-insensitive. A comparison on a metric the run has not
  logged is false for every operator, `!=` included.

The loop itself is `runWaiter` in `experiments_query.go`, written to be reused as is by
`tf mcp`'s `wait_for_run`.

### `tf mcp` — the experiment tools as an MCP server

```
tf mcp [--endpoint URL] [--token TOKEN] [--verbose]
```

Serves the `tf experiments` functionality to an AI agent over the
[Model Context Protocol](https://modelcontextprotocol.io/) (design contract:
`agent-features.md` §4). The agent's client starts `tf mcp` as a subprocess and talks to it on
stdin/stdout; there is no port and no daemon. Credentials follow the usual
[resolution order](#credential-resolution-order), so a machine where `tf login` was run needs
no configuration at all. They are resolved on the first tool call, not at startup: a server
started before `tf login` (or with no endpoint) keeps running, and each tool call answers an
error naming the remedy until credentials exist.

Register it with Claude Code:

```bash
claude mcp add thinkingface -- tf mcp
# or, without relying on a saved login:
claude mcp add thinkingface \
  -e THINKINGFACE_ENDPOINT=https://tf.example.com \
  -e THINKINGFACE_API_KEY=tf_xxxxxxxxxxxx \
  -- tf mcp
```

The JSON form (a project's `.mcp.json`, or any client that takes an `mcpServers` map):

```json
{
  "mcpServers": {
    "thinkingface": {
      "command": "tf",
      "args": ["mcp"],
      "env": {
        "THINKINGFACE_ENDPOINT": "https://tf.example.com",
        "THINKINGFACE_API_KEY": "tf_xxxxxxxxxxxx"
      }
    }
  }
}
```

Give the agent a token restricted to the repositories it works on (`agent-features.md` §3):
it can then annotate runs and edit notes there, and nothing else.

**Tools.** `repo` is always `ns/name`. Each answer is the API's JSON (`api-contract.md` §7),
both as compact JSON text and as `structuredContent`.

| Tool | Arguments (`?` = optional) | Does |
|---|---|---|
| `list_experiment_repos` | `author?`, `search?` | `GET /api/v1/experiments` |
| `list_projects` | `repo` | The repository's projects with run counts and metric goals |
| `list_runs` | `repo`, `project`, `group?`, `status?[]`, `tag?[]`, `archived?`, `sort?`, `order?`, `limit?` | Like `tf experiments runs --json`; `sort` takes the same spec |
| `get_run` | `repo`, `project`, `run` | `{"run": ...}` |
| `get_metrics` | `repo`, `project`, `runs[]`, `keys?[]`, `x?` (`step`\|`time`), `max_points?` | Series as `[x, y]` pairs. `max_points` **defaults to 200** (the HTTP API's default is 1000) to keep tool output small; at most 5000 |
| `wait_for_run` | `repo`, `project`, `run`, `until?` (default `status!=running`), `timeout_seconds?` (default 600, at most 3600), `ignore_stale?` | `runWaiter`, as `tf experiments wait`; answers `{"met", "reason", "run", "until"}`. A timeout is a normal result (`met: false, reason: "timeout"`): the agent calls again to keep waiting |
| `config_diff` | `repo`, `project`, `runs?[]`, `include_meta?` | Like `tf experiments diff --json` |
| `get_notes` | `repo`, `project` | `NOTES.md` with its `blob_sha` |
| `update_notes` | `repo`, `project`, `content`, `base_sha?`, `message?` | Replaces the notes. Passing `get_notes`' `blob_sha` as `base_sha` makes a concurrent edit a conflict instead of a lost write; omitting it overwrites |
| `annotate_run` | `repo`, `project`, `run`, `note?`, `tags?[]`, `archived?` | Only the fields given change; `tags` replaces the list |
| `set_metric_goals` | `repo`, `project`, `goals` (`{metric: "min"\|"max"\|""}`) | Merges goals; `""` removes one |

The tool descriptions (what the model reads) spell out defaults, limits and the `until` /
`sort` grammars; `initialize` also sends `instructions` explaining repositories / projects /
runs, the `until` grammar and metric goals. Read-only tools carry `readOnlyHint: true`, so a
client may let them run without asking.

**Protocol** (`mcp.go`, hand-rolled; no SDK dependency):

- JSON-RPC 2.0, one message per line, on stdin/stdout (MCP's stdio transport). Only
  protocol messages go to stdout; `--verbose` logs credential resolution, each request and
  its outcome to stderr, and without it `tf mcp` writes nothing to stderr.
- Protocol version `2025-06-18`. An `initialize` asking for `2025-03-26` or `2024-11-05` gets
  that version echoed (`structuredContent` is then left out); any other gets `2025-06-18`.
  Capabilities: `tools` only.
- Methods: `initialize`, `ping`, `tools/list`, `tools/call`; anything else is `-32601`. A
  line that is not JSON is `-32700` (id `null`), a batch array or a non-`2.0` message
  `-32600`. Notifications (no `id`) are never answered.
- `tools/call` naming an unknown tool, or with params that are not `{name, arguments}`, is a
  JSON-RPC `-32602`. Everything else that goes wrong — a missing or mistyped argument, an
  unknown argument (schemas are closed), an `until` that does not parse, a 4xx/5xx from the
  server, a notes conflict — is a normal result with `isError: true` and a sentence the
  model can act on, so the agent sees it and corrects itself.
- Every request runs on its own goroutine (stdout writes are serialised), so a
  `wait_for_run` holding for minutes does not block `ping` or other calls.
  `notifications/cancelled` cancels that request's context — the in-flight long poll is
  aborted — and the cancelled request gets no response. When stdin closes (MCP's shutdown
  signal), requests still in flight get 5 s to finish — enough for a piped
  `printf '...' | tf mcp` to get its answers — and are then cancelled (answered with
  `isError: true`, "cancelled"); `tf mcp` exits 0.

## Credential resolution order

For every command, `tf` decides the endpoint and token in the following priority order
(this is exactly the contract of `Resolve` in `backend/internal/tfcli/config`):

**endpoint**: the `--endpoint` flag > `TF_ENDPOINT` > `THINKINGFACE_ENDPOINT` >
`HF_ENDPOINT` > the config file's default endpoint. An error if none of these is set.

To make everything work purely from environment variables (without ever using
`tf login`), it's enough to set `THINKINGFACE_API_KEY` and `THINKINGFACE_ENDPOINT` (these
also coexist fine with `THINKINGFACE_ENDPOINT` / `THINKINGFACE_TOKEN`, which the Python
client `thinkingface.trackio` reads).

**token**: the `--token` / `--api-key` flag > `THINKINGFACE_API_KEY` > `TF_TOKEN` >
`THINKINGFACE_TOKEN` > a token saved in the config file for the resolved endpoint >
`HF_TOKEN` (but only when the normalized `HF_ENDPOINT` matches the resolved endpoint — a
safeguard against accidentally sending a token meant for the real huggingface.co to a
thinkingface server) > unset (anonymous; `tf up` refuses to run).

With `--verbose`, which path resolved each value (`from flag` / `from env TF_ENDPOINT`,
etc.) is printed to stderr.

## Config file

The save location is `$TF_CONFIG` (if set), then
`$XDG_CONFIG_HOME/thinkingface/config.json`, defaulting to
`~/.config/thinkingface/config.json`. Permissions are `0600` for the file and `0700` for
the directory; writes go through an atomic rename via a temp file. One token is saved per
endpoint, and the most recently logged-in endpoint becomes the default.

## About the displayed URL

The URL `tf up` prints at the end (and the `url` field in `--json`) is "the endpoint's
origin + the Web UI's path" (`/datasets/{ns}/{name}` / `/models/{ns}/{name}`). In a setup
like the docker compose development environment, where the API (`:8080`) and the Web UI
(`:3000`) are on different origins, mentally swap in the Web UI's origin (the path
portion can be used as-is). `commit_url` simply carries through the `commitUrl` the server
returned in its HF-compatible response.

## Relationship to `hf upload`

Because what `tf` speaks is exactly the HF-compatible API the thinkingface server already
exposes, anything `tf up` can do (aside from kind inference and automatic repository-card
generation) can also be done entirely with `huggingface_hub`'s `hf upload` / `HfApi`:

```bash
export HF_ENDPOINT=https://tf.example.com
export HF_TOKEN=tf_xxxxxxxxxxxx
export HF_HUB_DISABLE_XET=1
hf upload alice/imdb-ja ./imdb-ja . --repo-type dataset
```

`tf` is simply a wrapper that strips away managing `HF_ENDPOINT`/`HF_TOKEN`, choosing
`--repo-type`, and pre-creating the repository from that procedure — it introduces no
compatibility risk of its own.

## Design decision: why Go

Rather than adding commands to this repository's Python package
(`clients/python/thinkingface`), `tf` is implemented in the same Go module as the backend:

- It can be distributed as a **single static binary** — users don't need to set up a
  Python environment (venv / pip)
- It can **share type and test assets** with the backend (the `hub`/`local`/`config`
  packages are verified against the server's wire format in the same language, in the
  same repository)
- The Go gates in CI (`gofmt` / `go vet` / `go test`, the backend job in
  `.github/workflows/ci.yml`) apply to `tf` automatically — there's no need for a
  separate Python dependency-management or build pipeline

## Known limitations

- `tf up gs://...` is unsupported (produces a `gs:// import is not supported yet` error).
  For data in GCS, copy it locally first and then run `tf up`
- The server has no HF-compatible repo auto-create (the premise from
  `docs/dev/api-contract.md` §3 that "`create_repo` must precede `preupload`/`commit`" still
  holds), so `tf up` checks for and creates the repository itself before committing
- The command name `tf` can collide with Terraform (in environments that alias
  `terraform` to `tf`) or with TensorFlow's tooling. Watch out for shell alias
  configuration
