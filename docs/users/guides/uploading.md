# Uploading Files

Every way of getting files into thinkingface — Python, the CLI tools, git, the browser —
ends in the same place: a commit on a branch of a git repository, with large files stored as
Git LFS objects. This page covers each route, when to reach for which, and what the server
does once the bytes have landed.

If you have not created an access token yet, start with the
[Quickstart](../getting-started.md) and come back.

## Choose a route

| Route | Best for |
|---|---|
| `huggingface_hub` from Python | Uploading from a training or preprocessing script you are already writing |
| The `hf` CLI | One file, from a shell, when the Hugging Face client is already installed |
| The `tf` CLI | Registering a whole directory as a repository in a single command |
| `git push` | Repositories you clone and work in, and anything where you want history |
| The web UI | Small edits to Markdown or text files that already exist |

All of them need an access token with the **write** scope. See
[Authentication](../reference/authentication.md) for how scopes and roles interact.

## Set up the Hugging Face clients

`huggingface_hub` and `datasets` talk to thinkingface unmodified. Three environment
variables are all that changes:

```bash
export HF_ENDPOINT=http://localhost:8080
export HF_TOKEN=tf_xxxxxxxxxxxx
export HF_HUB_DISABLE_XET=1
```

!!! warning "Set `HF_ENDPOINT` before Python imports `huggingface_hub`"

    `huggingface_hub` resolves its default endpoint once, at import time. Exporting the
    variable in your shell (as above) is the reliable way to do it. If you must set it from
    inside Python, do it before the first `import huggingface_hub` anywhere in the process,
    or pass the endpoint explicitly: `HfApi(endpoint="http://localhost:8080",
    token="tf_xxxxxxxxxxxx")`.

### Why `HF_HUB_DISABLE_XET=1` is required

Since `huggingface_hub` 1.0, the client prefers the Xet protocol for transferring large
files whenever the `hf_xet` package happens to be installed — and it is a dependency of
several common installs, so you may have it without having asked for it.

thinkingface moves large files over Git LFS and does not implement Xet. Rather than let the
client fail somewhere obscure, the two Xet endpoints answer `501` with an explanation:

```text
thinkingface transfers large files over Git LFS, not Xet. Set HF_HUB_DISABLE_XET=1 in the
environment (or call thinkingface.login(), which sets it for you) and retry.
```

Setting the variable makes `huggingface_hub` use the LFS path instead. Nothing else about
your code changes, and the resulting files are identical.

### The `thinkingface.login()` helper

The repository ships a small Python package that sets those variables for you, along with a
trackio-compatible client for experiment tracking:

```bash
pip install -e clients/python
```

```python
import thinkingface

thinkingface.login("http://localhost:8080", token="tf_xxxxxxxxxxxx")
```

It sets `HF_ENDPOINT`, `HF_TOKEN` and `HF_HUB_DISABLE_XET` for the current process and calls
`huggingface_hub.login()` so the token is cached the way `hf auth login` would cache it.
Because of the import-time resolution described above, call it before `huggingface_hub` is
imported, or keep using the shell variables and treat the helper as a convenience.

## Upload a single file

```python
from huggingface_hub import HfApi, upload_file

api = HfApi()
api.create_repo("admin/imdb-reviews", repo_type="dataset", exist_ok=True)

upload_file(
    path_or_fileobj="train.parquet",
    path_in_repo="data/train.parquet",
    repo_id="admin/imdb-reviews",
    repo_type="dataset",
    commit_message="Add training data",
)
```

`repo_type` defaults to `model`, so datasets need it spelled out on every call. A new
repository is created with `main` as its default branch and an initial commit containing a
`README.md` and a `.gitattributes` — see [LFS routing](#how-files-are-routed-to-git-lfs)
below for what that second file does.

!!! note "`private=True` is accepted but has no effect"

    thinkingface has no repository visibility setting. Every repository on an instance is
    readable by everyone who can reach it, including anonymous callers. What permissions
    control is *writing*. If that does not match your deployment, keep the whole instance
    behind your network boundary. [Downloading Files](downloading.md#who-can-read-what)
    covers the consequences on the read side.

## Upload a folder

`upload_folder` walks a local directory and commits everything in it:

```python
from huggingface_hub import HfApi

api = HfApi()
api.create_repo("acme/sentiment-base", repo_type="model", exist_ok=True)

api.upload_folder(
    repo_id="acme/sentiment-base",
    folder_path="out/checkpoint",
    commit_message="Add fine-tuned checkpoint",
)
```

Each file is classified as regular or LFS by the server before the transfer starts, so a
folder mixing `config.json` with `model.safetensors` needs no special handling.

## Upload from the shell with `hf`

The `hf` CLI that ships with `huggingface_hub` uses the same endpoints, so it works once the
environment variables are exported:

```bash
hf upload admin/imdb-reviews ./train.parquet data/train.parquet --repo-type dataset
```

The arguments are, in order, the repository, the local path, and the path inside the
repository. As in Python, `--repo-type dataset` is required for datasets.

## Push a whole directory with `tf`

`tf` is thinkingface's own client: a single static binary that turns "register this
directory" into one command. It creates the repository if it does not exist, infers whether
the contents are a dataset or a model, names the repository after the directory, and pushes
everything as a single commit.

```bash
tf login http://localhost:8080
tf up ./imdb-ja
```

Pushing to an existing repository sends only the difference. `--dry-run` shows what would
happen without changing anything, and `--to` overrides the destination:

```bash
tf up ./imdb-ja --to acme/imdb-reviews --dry-run
```

In CI, skip `tf login` and pass credentials in the environment instead:

```bash
export THINKINGFACE_ENDPOINT=http://localhost:8080
export THINKINGFACE_API_KEY=tf_xxxxxxxxxxxx
tf up ./imdb-ja
```

Every command, flag and credential-resolution rule is in the
[tf CLI reference](../reference/tf-cli.md).

## Push with git

A repository is an ordinary git remote. Clone it, commit, push:

```bash
git clone http://localhost:8080/datasets/admin/imdb-reviews.git
cd imdb-ja
git add data/train.parquet
git commit -m "Add training data"
git push origin main
```

This is the one route where *your* local `.gitattributes` and `git lfs track` settings decide
what goes to LFS, rather than the server. [Working with Git](git.md) covers credentials, LFS
setup, SSH and branches in full.

## Edit a file in the browser

Markdown and text files that already exist in a repository can be edited and committed from
the repository page, which is the quickest way to fix a README. LFS-tracked files cannot be
edited this way, and edits must target a branch rather than a commit SHA. See
[Browsing the Web UI](web-ui.md).

## How files are routed to Git LFS

Two rules decide whether a file's bytes go into the git object database or into object
storage as an [LFS object](../concepts.md):

1. **`.gitattributes` patterns win.** Every repository is created with a default
   `.gitattributes` covering the formats that are always large, so LFS routing works from the
   very first upload:

    ```text
    *.safetensors filter=lfs diff=lfs merge=lfs -text
    *.parquet filter=lfs diff=lfs merge=lfs -text
    *.bin filter=lfs diff=lfs merge=lfs -text
    *.gguf filter=lfs diff=lfs merge=lfs -text
    *.pt filter=lfs diff=lfs merge=lfs -text
    *.onnx filter=lfs diff=lfs merge=lfs -text
    ```

    That is an excerpt — around three dozen patterns are seeded, including `*.ckpt`, `*.h5`,
    `*.npy`, `*.npz`, `*.tar.*`, `*.zip`, `*.zst` and `*tfevents*`. Read the repository's own
    `.gitattributes` for the full list, and edit it like any other file to add your own
    patterns. Later lines win over earlier ones, exactly as in git.

2. **Anything 10 MiB or larger goes to LFS anyway**, when no pattern matches it. This keeps
   the bare repository small enough to clone cheaply no matter what gets committed.

Note that a matching `-filter=lfs` rule forces a file to stay a regular git blob regardless
of size.

Who applies these rules depends on the route:

| Route | Decided by |
|---|---|
| `huggingface_hub`, `hf`, `tf` | The server, from the repository's `.gitattributes` at the target revision plus the 10 MiB threshold |
| `git push` | Your local git-lfs installation, from the `.gitattributes` in your working tree |

Either way, LFS object bytes never pass through the API server in a production deployment:
the client gets a signed URL and transfers directly with the bucket.

## What happens after an upload

Uploading is not quite the end of the story. When a branch changes, a background worker picks
the push up and, for that revision:

1. Publishes every non-LFS file to `blobs/{sha}` in object storage. (LFS objects are already
   there — that is where the client uploaded them.)
2. Rebuilds the file index, which is what the file tree, the size totals, and the
   `gcloud storage cp` script are generated from.
3. Reads the footer of every `.parquet` file to record its schema and row count, which is
   what makes a Parquet file browsable in the [dataset viewer](dataset-viewer.md).
4. Parses the YAML front matter of `README.md` into the repository card — license, tags,
   `lineage:` edges — for the default branch.
5. Refreshes the run index, if the repository holds experiment metrics.

This normally completes within a second or so of the push. If a Parquet file is not yet
browsable, or the "GCS access" script comes back with fewer files than you expect, reload the
page: the indexing is very likely still in flight.

!!! note "Only branch pushes are indexed"

    The worker is triggered by branch tips moving. Pushing a tag on its own does not schedule
    indexing work, so a revision that exists only as a tag can be downloaded through git and
    the resolve URLs but may not appear in the file index that the bucket-access script is
    built from.

## When an upload is refused

| Response | What it means |
|---|---|
| `401 authentication required to write to {repo}` | No credentials reached the server. Check `HF_TOKEN`, or the git credential helper |
| `403 this token is read-only` | The token has the `read` scope. Issue one with `write` |
| `403 you do not have write access to {repo}` | The token is valid but your account has no write role in that namespace. For an organization, ask an admin to raise your role — see [Organizations](organizations.md) |
| `403 {repo} is archived and read-only` | Unarchive it in the repository settings first |
| `501 xet_not_supported` | `HF_HUB_DISABLE_XET=1` is not set — see [above](#why-hf_hub_disable_xet1-is-required) |
| `400 commits must target a branch, not a commit SHA` | The upload named a commit as its revision. Push to a branch |

## Next steps

- [Downloading Files](downloading.md) — the retrieval side of everything on this page.
- [Working with Git](git.md) — clone, LFS, SSH, branches and tags.
- [Viewing Datasets](dataset-viewer.md) — what the browser does with the Parquet files you
  just pushed.
