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

- If `ENDPOINT` is omitted, the config file's default endpoint is used. If there is none
  either, and stdin is a terminal, it prompts interactively.
- Passing `--token` verifies that token via `whoami` and saves it as-is
  (`--token -` reads one line from stdin as the token).
- Without `--token`, it logs in with username/password and issues and saves a new
  write-scoped personal access token. The password is entered with hidden input on a
  terminal, or, when piped, passed as one line on stdin via `--password-stdin`.
- A warning is shown if the issued token's scope turns out to be `read` (`tf up` needs
  write scope).

### `tf logout [ENDPOINT]`

Deletes saved credentials. A token that `tf login` itself issued (as opposed to one
pasted in via `--token`) — i.e. one issued via username/password — is also revoked
server-side on a best-effort basis.

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
`admin`/`write` permission).

### `tf up PATH [flags]`

The core command. Pushes the contents of PATH (a file or directory) to a repository as a
single commit, creating the repository first if it doesn't exist.

```
tf up PATH [--to NS/NAME|NAME] [--kind dataset|model] [--rev BRANCH]
           [-m/--message MSG] [--license L] [--tag T ...] [--desc TEXT]
           [--include GLOB ...] [--exclude GLOB ...] [--delete] [--dry-run]
           [--workers N] [--quiet] [--json]
```

| Flag | Default | Meaning |
|---|---|---|
| `--to NS/NAME` or `NAME` | your namespace + PATH's directory name | The destination repository. Adding a `datasets/` or `models/` prefix also pins the kind |
| `--kind dataset\|model` | inferred from contents | Explicitly pins the kind (takes priority over the `--to` prefix) |
| `--rev` | `main` | Branch to push to |
| `-m`, `--message` | `Upload N files with tf` | Commit summary |
| `--license` | (unset) | The repository card's `license` |
| `--tag` | (unset) | The repository card's `tags` (repeatable; comma-separated values can also be given together: `--tag a,b --tag c`) |
| `--desc` | (unset) | The repository card's `description` (also becomes the opening paragraph of the body in a generated README) |
| `--include` / `--exclude` | include everything | Narrows the file set via shell globs (repeatable) |
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

**README handling**: if any of `--license`/`--tag`/`--desc` is given and there's no local
`README.md`, a `README.md` with a repository card is generated from those values and
included in the upload. If a local `README.md` exists, only the specified values are
merged into it while the existing frontmatter is preserved (the body and key ordering are
kept as-is). If none of the card flags are given, the README is left untouched entirely
(for a brand-new repository, the server's initial README is simply left as-is).

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

### `tf version`

Prints `tf <version> (<GOOS>/<GOARCH>)`.

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
