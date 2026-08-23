# Core Concepts

thinkingface has a small model underneath it: everything is a git repository, every repository
belongs to a namespace, and every byte lands in object storage under a key derived from its
content. Once those three ideas are clear, the rest of the site is detail. This page explains
the model; the guides show you the commands.

## Repositories

A repository is a bare git repository plus an index the server keeps about it — its file list,
its card, its Parquet schemas, its size. Nothing else. There is no separate "artifact registry"
layer, and no database row that holds a copy of your data.

Repositories come in two kinds:

| Kind | Holds | Addressed as |
|---|---|---|
| `dataset` | Training and evaluation data, usually Parquet, CSV or JSONL | `/datasets/{ns}/{name}` |
| `model` | Checkpoints and their config files — safetensors, PyTorch, GGUF, ONNX | `/models/{ns}/{name}` |

The kind changes how clients address the repository and what the web UI offers you — a Parquet
table view for datasets, a checkpoint metadata panel for models — but not how it works. Both are
git, both use Git LFS, both are pushed and pulled the same way. Experiment logs are dataset
repositories too; see [Experiments](#experiments-projects-and-runs) below.

Every new repository is seeded with two files: a `.gitattributes` carrying the default LFS rules,
and a `README.md` with YAML front matter, so LFS routing works from the very first upload. The
rules depend on the kind — a dataset also tracks audio, image and video files, which a model
repository leaves as ordinary blobs. See
[How files are routed to Git LFS](guides/uploading.md#how-files-are-routed-to-git-lfs).

!!! note "There is exactly one write path"

    Editing a Markdown file in the browser, `upload_file` from Python, `tf up`, and `git push` all
    end as a git commit on a branch. History never diverges depending on which tool you used, and
    `git log` is always the whole truth about a repository.

There is no per-repository visibility setting. Anyone who can reach the server can read every
repository; access tokens and namespace roles govern who can *write*. Plan your network boundary
accordingly — see [Deployment](self-hosting/deployment.md).

## Namespaces

Every repository is named `{namespace}/{name}`. The namespace is the owner segment, and users and
organizations share a single namespace space: if the user `acme` exists, no organization can also
be called `acme`, and vice versa. A name is claimed exactly once, by whichever kind of owner got
there first.

A namespace name is fixed at creation. Usernames and organization IDs cannot be renamed, because
the name is the address of everything underneath it. If you need a repository to live somewhere
else, transfer the repository rather than renaming its owner.

Names are unique case-insensitively, and `/{ns}` is the one page for either kind of owner — the
profile, plus Models, Datasets and Experiments tabs, and a Members tab for organizations. Visiting
a different spelling redirects to the canonical one.

Organizations add roles on top: `admin`, `write` and `read`, described in
[Organizations](guides/organizations.md).

## Revisions

A revision is a branch name, a tag, or a commit SHA. The default branch is `main`.

Anywhere a revision can appear, all three spellings work — the file tree URL
(`/datasets/admin/imdb-reviews/tree/main/data`), a `revision=` argument in `huggingface_hub`, a
`--rev` on the `tf` CLI. Pinning a SHA gives you a snapshot that cannot move under you.

Pushing to any branch or tag indexes that revision's files and publishes their bytes to storage.
A few things are read from the default branch only: the repository card, the description and tags
shown in listings, and the declared lineage. That is what makes the default branch the
repository's public face while feature branches stay browsable.

## How files are stored

This is the part that differs most from a hosted hub, and it is worth understanding because it is
what lets you read your data straight out of your own bucket.

Three layers hold your files:

**1. The git repository.** Commits, refs and history, plus the bytes of small text files as
ordinary git blobs. This is what `git clone` gives you.

**2. Git LFS.** Large files are not stored in git history at all. Git holds a small pointer file;
the payload goes to object storage at `lfs/{oid}`, keyed by the SHA-256 of its contents.

**3. Published blobs.** After every push, the server also publishes the bytes of the *non*-LFS
files to `blobs/{sha}`, keyed by their git blob SHA. That way, everything in a revision — big and
small — is reachable in the bucket, not only the LFS half.

What decides whether a file goes through LFS is `.gitattributes`, exactly as on the Hub. The
default the server writes covers the usual suspects: `*.parquet`, `*.safetensors`, `*.bin`,
`*.gguf`, `*.ckpt`, `*.pt`, `*.onnx`, `*.zip`, `*.tar.*` and more. When a file matches no rule at
all and is uploaded through the API — from Python, the `tf` CLI, or the browser — anything of
10 MiB or more is routed to LFS anyway. Over plain `git push`, your local `git-lfs` applies
`.gitattributes` itself, so committing a new large format means tracking it first.

### Why the bucket has no folders

Both storage layers are **content-addressed**: the key is computed from the bytes, and depends on
neither the namespace, nor the repository name, nor the branch.

```text
gs://{bucket}/
├── lfs/{oid[0:2]}/{oid[2:4]}/{oid}      LFS payloads, keyed by SHA-256 of the content
├── blobs/{sha[0:2]}/{sha[2:4]}/{sha}    non-LFS file bytes, keyed by git blob SHA
└── ...                                  server-internal keys (git history, upload scratch)
```

Looking inside the bucket, you cannot tell which repository an object belongs to. That is
deliberate, and it buys three things:

- **Identical content is stored once**, across every repository and every branch that contains it.
- **Renaming or transferring a repository moves no bytes.** The keys never mentioned its name.
- **Objects are immutable.** A key is written once and never overwritten, so anything that read it
  before still reads the same bytes.

The cost is that there is no human-readable folder to `cp -r`. The human-readable layout is
reconstructed on the consumer side instead: the **GCS access** dialog on a repository page — and
the API behind it — generates a `gcloud storage cp` script that maps every content-addressed key
in a revision back to its original path.

```sh
#!/bin/sh
set -eu
DEST="${DEST:-./imdb-ja}"
cp_one 'gs://my-bucket/blobs/3f/2a/3f2a9c…' "$DEST"/'README.md'
cp_one 'gs://my-bucket/lfs/9b/1d/9b1de4…' "$DEST"/'data/train-00000-of-00002.parquet'
```

Point `DEST` at a local directory and you get a download; point it at a `gs://` prefix and you get
a bucket-to-bucket copy. For a revision containing Parquet, the same response also carries a
DuckDB query over the same keys, and those keys can be handed straight to BigQuery external
tables. [Downloading Files](guides/downloading.md) walks through all of it.

## Repository cards

The card is the YAML front matter at the top of `README.md` — the same convention the Hugging Face
Hub uses, so a card written for the Hub carries over unchanged.

```yaml
---
license: apache-2.0
tags:
  - sentiment
  - japanese
task_categories:
  - text-classification
---
```

The card is not decoration. On every push to the default branch the server re-reads it, and it
drives:

- **Search and filters** — `tags`, `license`, and the task (`pipeline_tag` on a model card,
  `task_categories` on a dataset card) are the facets in the Models and Datasets listings.
- **The description** in listings and search results, taken from a `description` field if the card
  has one, and otherwise from the first real paragraph of the README body.
- **Dataset viewer hints** — `dataset_info.features` tells the viewer which columns are images,
  audio or class labels rather than plain scalars.
- **Lineage** — a `lineage:` block (or the Hub's own `datasets:`, `base_model:`, `model-index:`
  fields) records which dataset a model was trained on, which checkpoint it started from, and
  which run produced it, so the UI can walk provenance in both directions.
- **The experiment marker** — a `trackio` tag, or a `metrics.parquet` in the repository, is what
  makes a dataset repository show up under Experiments.

Because the card is just the top of a file in git, you edit it like any other file: in the
browser, or in a commit. Front matter that fails to parse never fails an upload — the README is
shown as-is and the card comes out empty.

## Access tokens

An access token is a `tf_...` string you create at **Settings → Access tokens** and copy once; the
value is never shown again. Each token has a scope, `read` or `write`, and belongs to one user.

One token is every credential you need, because thinkingface accepts the same value in all the
places a client might present it:

| Used as | How |
|---|---|
| git over HTTP | the password in HTTP Basic auth (any username) |
| REST API | `Authorization: Bearer tf_...` |
| `huggingface_hub` / `datasets` | `HF_TOKEN` |
| `tf` CLI | saved by `tf login`, or `THINKINGFACE_API_KEY` in the environment |
| The trackio-compatible client | `THINKINGFACE_TOKEN` |

Signing in to the web UI is separate: the browser gets a session cookie, not a token. And git over
SSH uses a public key you register instead of a token. What a token lets you *do* is the
intersection of its scope and your permissions on the namespace you are writing to.
[Authentication](reference/authentication.md) has the full picture.

## Experiments: projects and runs

Experiment tracking is not a separate subsystem. An experiment lives in an ordinary dataset
repository — by default `{your-namespace}/trackio-metrics` — and the source of truth is a Parquet
file inside it.

Two units organise the data:

- A **run** is one execution of a training script. It has a name, the config it started with, a
  status (`running`, `finished`, `failed`), and the metric series it logged step by step.
- A **project** groups runs you want to compare. On disk a project is the directory holding a
  `metrics.parquet`, so `mnist/metrics.parquet` is the project `mnist`.

Metrics reach that file by either of two routes. A trackio script pointed at your instance syncs
its Parquet up on a timer, and the server indexes what it finds. Or the bundled shim posts each
`log()` call to an ingest endpoint, which buffers points for live charts and periodically commits
them into the very same Parquet file. Either way, hardware telemetry is namespaced under a
`system/` prefix so it can never collide with a metric your script emits.

The consequence is the useful part: your experiment history is version-controlled in git, comes
along when you clone the repository, and is readable with DuckDB or `gcloud storage` like any
other dataset. The charts in the web UI are a view over that file, not a database only the UI can
read. See [Tracking Experiments](guides/experiments.md).

## Where to go next

- [Quickstart](getting-started.md) — get an instance running and put a dataset in it.
- [Uploading Files](guides/uploading.md) and [Downloading Files](guides/downloading.md) — these
  concepts as commands.
- [Compatibility](reference/compatibility.md) — precisely which parts of the Hugging Face Hub API
  thinkingface implements, and which it does not.
