# Compatibility

thinkingface exposes the same protocols the tools in the Hugging Face ecosystem already
speak — no forked client, no thinkingface-specific SDK for the common paths. This page lists
what's actually verified to work, sourced from the `e2e/` pytest suite that runs against a
live instance, and states plainly where thinkingface diverges from huggingface.co.

## `huggingface_hub`

Point `HF_ENDPOINT` at your instance, set `HF_TOKEN` to an access token, and use `HfApi` (or
the equivalent top-level functions) exactly as you would against `huggingface.co`:

```bash
export HF_ENDPOINT=http://localhost:8080
export HF_TOKEN=tf_xxxxxxxxxxxx
export HF_HUB_DISABLE_XET=1
```

| Operation | Verified |
|---|---|
| `whoami()` | Yes |
| `create_repo()` | Yes (both `model` and `dataset`) |
| `upload_file()` | Yes, both a plain file and a Git LFS-routed file (for example `*.parquet`, `*.safetensors`) |
| `list_repo_files()` | Yes |
| `list_repo_tree()` | Yes, including directory entries with `recursive=True` |
| `hf_hub_download()` | Yes |
| `repo_info()` | Yes, and returns a 404-equivalent error after `delete_repo()` |
| `delete_repo()` | Yes |
| `list_organization_members()` | Yes |
| `get_user_overview()` / `get_organization_overview()` | Yes, and each 404s for the other kind of namespace |
| `list_repo_refs()` | Yes, branches and tags with their tips |
| `create_branch()` / `delete_branch()` | Yes, including `exist_ok=True` and branch names containing `/`. The default branch cannot be deleted |
| `create_tag()` / `delete_tag()` | Yes, including `exist_ok=True`. A `tag_message` creates a real annotated tag |
| `list_repo_commits()` | Yes, newest first, with `revision=` and pagination |

`upload_folder()` and `snapshot_download()` are not exercised by the test suite, so they are
left out of this table rather than assumed. If you rely on either, verify against your own
instance before depending on it.

One thing to know about branches and tags created through the API: **creating a branch schedules
the same background indexing a push does, and creating a tag does not** — exactly like
`git push <branch>` versus `git push <tag>`. See [Working with Git](../guides/git.md#branches-tags-and-revisions).

## `datasets`

`datasets.load_dataset("parquet", data_files=...)` against a file downloaded with
`hf_hub_download()` is verified. Loading directly by repository ID
(`load_dataset("admin/imdb-reviews")`) works the same way `huggingface_hub` resolution does, since
`datasets` itself calls into `huggingface_hub` for that.

## git and Git LFS

`git clone` and `git push` work over both HTTP and, when the instance operator has enabled it,
SSH — including LFS objects, which are transferred as real files rather than pointers on
checkout. See [Working with Git](../guides/git.md) for the walkthrough and
[Authentication](authentication.md#ssh-keys) for SSH key setup.

**Only the git smart HTTP protocol is supported.** A client that falls back to the dumb HTTP
protocol gets an explicit error telling it to use a recent git client, rather than failing
obscurely.

## `gcloud storage` / plain GCS access

After a push, files are published to a content-addressed object store: non-LFS blobs at
`blobs/{sha}` (keyed by the git blob SHA-1) and LFS objects at `lfs/{oid}` (keyed by the
SHA-256 content hash) — both deduplicated across every repository, so nothing in the bucket
is named after a namespace or path. A repository page's sidebar has a **GCS access** dialog
that generates the mapping from repository paths to these keys as a ready-to-run
`gcloud storage cp` script (and a DuckDB `read_parquet()` snippet for `.parquet` files),
letting you fetch data straight from storage without going through the API at all.

## trackio

The `thinkingface.trackio` shim is a drop-in replacement for `trackio`'s `init`/`log`/`finish`
API. Runs, configs, and metrics are extracted both from the Parquet files it backs up to a
dataset repository (`{project}/metrics.parquet` + `{project}/aux/configs.parquet`) and, while
a run is live, from the native ingest API — so metrics show up in the run list and charts
without waiting for a flush. See [Tracking Experiments](../guides/experiments.md) for details.

## Known incompatibilities and limitations

**There are no private repositories.** thinkingface has no per-repository visibility
setting at all: anyone who can reach the instance can read, clone and download every
repository on it, signed in or not. `create_repo(..., private=True)` and
`visibility="private"` are accepted so that unmodified `huggingface_hub` code keeps working,
but neither changes what is created, and repository listings always report `private: false`.
Tokens and organization roles gate **writes** only. Plan for this: the network boundary
around the instance is the only read boundary you have.

**Xet is not supported.** Since `huggingface_hub` 1.0, large-file transfers prefer the Xet
protocol whenever the `hf_xet` package is installed. thinkingface transfers large files over
Git LFS only — Xet endpoints answer with an explicit `501` telling the client what to do
instead of a confusing failure. Set `HF_HUB_DISABLE_XET=1` in the environment before using
`huggingface_hub` against a thinkingface instance (the `thinkingface` Python package's
`login()` helper sets this for you).

**SQLite mode has narrower search semantics than PostgreSQL.** An instance can run on either
PostgreSQL or SQLite (`DATABASE_URL`'s scheme selects which). Under SQLite:

- Only a single process / single writer connection is supported — concurrent writes from
  multiple replicas are not.
- The HF-compatible `search=` substring match (PostgreSQL's `ILIKE`) becomes a plain `LIKE`,
  which is case-insensitive for ASCII characters only — no Unicode case folding.
- Web UI full-text search runs on SQLite's FTS5 (`unicode61` tokenizer) rather than
  PostgreSQL's `tsvector`, and does not produce identical results (no language-specific
  stemming, for one).

None of this affects protocol compatibility — a client can't tell which database backs the
instance it's talking to — but search results and concurrent-write behavior can differ
between two instances of the same version.

## See also

- [tf CLI](tf-cli.md) — a thinkingface-specific client built on the same API described here
- [Authentication](authentication.md) — tokens, SSH keys, and how each protocol uses them
