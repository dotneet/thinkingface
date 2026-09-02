# tf CLI

`tf` is a single static-binary command-line client for registering datasets and models with
thinkingface. This page is the complete reference for its commands, flags, credential
resolution, and configuration file. For a first walkthrough, see
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

Every subcommand except `version` accepts these flags:

| Flag | Meaning |
|---|---|
| `--endpoint URL` | Server URL. If omitted, follows the [credential resolution order](#credential-resolution-order) |
| `--token TOKEN` / `--api-key KEY` | An access token; both flags set the same value. If omitted, follows the resolution order |
| `--verbose` | Print how the endpoint and token were resolved to stderr |

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

### `tf logout [ENDPOINT]`

Forgets the saved credentials for a server (default: the configured default endpoint). If the
saved token was minted by `tf login` itself (as opposed to pasted in with `--token`), it is
also revoked server-side on a best-effort basis.

### `tf whoami`

Shows the identity behind the current token: name, email, the token's scope, organization
memberships, and the namespaces you can push to (yourself plus any organization where you
hold `admin` or `write`).

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
| `--include` | include everything | Only include files matching this glob (repeatable). Not a shell glob run through the shell — `tf` matches it itself, with `**` matching any number of path segments (`data/**`, `**/*.parquet`) and, for a pattern with no `/` at all, also tried against just the file's base name (`*.parquet` matches `data/train.parquet` too) |
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

### `tf version`

Prints `tf <version> (<GOOS>/<GOARCH>)`.

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
`tf login` — `tf logout` uses it to decide whether there's anything to revoke server-side.

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
- [Compatibility](compatibility.md) — what's verified to work through `huggingface_hub` and git
