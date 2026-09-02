# Downloading Files

There are two fundamentally different ways to get files out of thinkingface: through the
server, which is what `huggingface_hub`, `datasets`, git and plain HTTP all do, or straight
out of the object store, bypassing the server entirely. This page covers both, and when the
second one is worth the extra step.

## Choose a route

| Route | Best for |
|---|---|
| `hf_hub_download` | One known file, with local caching |
| `snapshot_download` | Every file of a revision, into a local directory |
| `datasets.load_dataset` | Reading a dataset repository as a `Dataset` object |
| A `resolve` URL | `curl`, `wget`, or anything that speaks HTTP |
| `git clone` | The whole history, and a working tree you can commit in |
| The generated `gcloud storage cp` script | Bulk restores, and copying between buckets without moving bytes through the server |

## Set up the Hugging Face clients

Same three environment variables as on the upload side:

```bash
export HF_ENDPOINT=http://localhost:8080
export HF_TOKEN=tf_xxxxxxxxxxxx
export HF_HUB_DISABLE_XET=1
```

Export them before Python starts — `huggingface_hub` reads its default endpoint once, at
import time. [Uploading Files](uploading.md#set-up-the-hugging-face-clients) has the details
and covers the `thinkingface.login()` helper.

## Download one file

```python
from huggingface_hub import hf_hub_download

path = hf_hub_download(
    repo_id="admin/imdb-reviews",
    repo_type="dataset",
    filename="data/train.parquet",
)
```

The return value is a path into the local `huggingface_hub` cache, so calling it again for an
unchanged file costs one request and no transfer. Pin a revision with `revision=`, which
accepts a branch name, a tag or a commit SHA:

```python
path = hf_hub_download(
    repo_id="acme/sentiment-base",
    filename="model.safetensors",
    revision="v1.0",
)
```

## Download a whole revision

```python
from huggingface_hub import snapshot_download

local_dir = snapshot_download(repo_id="admin/imdb-reviews", repo_type="dataset")
```

`snapshot_download` asks the server for metadata about every path in one batch and then
fetches the files, so it is considerably better than looping over `hf_hub_download` yourself.
`allow_patterns` / `ignore_patterns` narrow what comes down.

## Load a dataset

`datasets` works against thinkingface with no code changes:

```python
from datasets import load_dataset

ds = load_dataset("admin/imdb-reviews")
```

Split detection works the usual way: a repository whose files are laid out as
`data/train-*.parquet` and `data/test-*.parquet` resolves into `train` and `test` splits
without further configuration.

If you would rather be explicit — or the repository does not follow a layout `datasets`
recognises — download the file first and load it by path:

```python
from datasets import load_dataset
from huggingface_hub import hf_hub_download

path = hf_hub_download(
    repo_id="admin/imdb-reviews", repo_type="dataset", filename="data/train.parquet"
)
ds = load_dataset("parquet", data_files=path)
```

## Download over plain HTTP

Every file is reachable at a `resolve` URL, which is the same URL shape the Hugging Face Hub
uses. Datasets carry a `/datasets` prefix; models sit at the root:

```text
http://localhost:8080/datasets/{namespace}/{name}/resolve/{revision}/{path}
http://localhost:8080/{namespace}/{name}/resolve/{revision}/{path}
```

```bash
curl -L -H "Authorization: Bearer tf_xxxxxxxxxxxx" \
  -o train.parquet \
  http://localhost:8080/datasets/admin/imdb-reviews/resolve/main/data/train.parquet
```

Three things to know about these URLs:

- **Follow redirects.** A regular file is streamed straight from git. An LFS file redirects
  to a time-limited signed URL for the object in the bucket, so the transfer never touches
  the API server. (Against the local storage emulator, which cannot sign URLs, the server
  streams the object through instead.) `curl -L` covers both cases.
- **`HEAD` works** and is cheap: it returns the size and the object's identity without
  transferring anything.
- **Everything is served as an attachment**, with `Content-Disposition: attachment` and
  `X-Content-Type-Options: nosniff`. A `.html` file pushed to a repository downloads rather
  than rendering, deliberately.

## Restore a revision straight from the bucket

Objects in the bucket are stored by **content address** — an LFS object at `lfs/{oid[0:2]}/{oid[2:4]}/{oid}`, an
ordinary file at `blobs/{sha[0:2]}/{sha[2:4]}/{sha}`. Nothing in the bucket is named after a namespace, a
repository or a path, so there is no directory tree to `cp -r`. What puts the names back is a
script the server generates on demand, which maps each content-addressed object to the path
it holds in that revision.

Use it when:

- You are restoring a large revision and would rather the bytes did not travel through the
  API server at all.
- The destination is another bucket. Pass a `gs://` prefix as `DEST` and it becomes a
  server-side bucket-to-bucket copy instead of a download.
- You want to hand a colleague something reproducible: the script is deterministic, and the
  file list it is built from is sorted by path.

### From the web UI

Open the repository page and choose **GCS access**. The dialog shows how many files the
revision has and how large it is, with a copyable `gcloud storage script` and — when the
revision contains Parquet files — a **DuckDB** tab holding a matching `read_parquet()` query.
Individual files in the file browser have their own **GCS access** action that copies a
single-file `gcloud storage cp` command.

### From the API

```bash
curl -s http://localhost:8080/api/v1/repos/dataset/admin/imdb-reviews/gcs/main \
  | jq -r .gcloud_script | DEST=./imdb-ja sh
```

The response also carries the file listing (`files`, each with its `gs://` URI, size and an
`lfs` flag) and the DuckDB snippet (`duckdb_snippet`), so you can build your own tooling on
it rather than running the script as-is.

The script itself looks like this:

```bash
#!/bin/sh
# thinkingface: datasets/admin/imdb-reviews @ main -- 3 files, 536871936 bytes
# Objects are content-addressed; this script lays them out under DEST.
# DEST may be a local directory or a gs:// prefix.
set -eu
DEST="${DEST:-./imdb-ja}"
cp_one() {
  case "$DEST" in gs://*) ;; *) mkdir -p "$(dirname "$2")" ;; esac
  gcloud storage cp "$1" "$2"
}
cp_one 'gs://my-bucket/blobs/3f/2a/3f2a9c…' "$DEST"/'README.md'
cp_one 'gs://my-bucket/lfs/9b/1d/9b1de4…' "$DEST"/'data/train.parquet'
```

`DEST` defaults to a directory named after the repository. Because every `gs://` key in it is
a content address, the same keys can be handed to anything else that reads from object
storage — a DuckDB `read_parquet()`, a BigQuery external table, a training job on another
machine.

### Two caveats

- **The script lists what has been indexed, not what git knows.** It is generated from the
  file index the background worker rebuilds after each push, so a revision that was pushed
  moments ago — or one that exists only as a tag, which does not schedule indexing — can come
  back with an empty file list rather than an error. Wait a moment and ask again.
- **Bucket access is separate from thinkingface access.** Generating the script is an
  ordinary read of the API; *running* it needs credentials for the bucket itself, which
  thinkingface does not issue. In the local compose setup that means pointing the `gcloud`
  CLI at the storage emulator:

    ```bash
    gcloud config set api_endpoint_overrides/storage http://localhost:4443/storage/v1/
    ```

## Clone the repository

For a working tree with history, clone it — `git clone` over HTTP or SSH, with Git LFS
pulling the real bytes of large files as they are checked out. See
[Working with Git](git.md).

## Who can read what

thinkingface has **no repository visibility setting**. There is no private/public flag on a
repository, and no per-repository read permission: every repository on an instance is
readable by anyone who can reach the server, including callers presenting no credentials at
all. `private=True` passed to `create_repo` is accepted for client compatibility and does
nothing.

What the permission system controls is writing. Treat the instance itself as the security
boundary and keep it inside your network perimeter if the contents are sensitive.

| Route | Credentials needed |
|---|---|
| `hf_hub_download`, `snapshot_download`, `load_dataset` | None to read. `HF_TOKEN` is still worth setting so the same script can write |
| `resolve` URLs | None to read. `Authorization: Bearer tf_...` is accepted |
| `git clone` over HTTP | None to read; a token to push |
| `git clone` over SSH | A registered SSH key, always — the SSH transport authenticates every connection |
| The `gcloud storage cp` script | None to generate it; credentials for the bucket itself to run it |

See [Authentication](../reference/authentication.md) for tokens, scopes and roles.

## Next steps

- [Working with Git](git.md) — clone, LFS and revisions in detail.
- [Viewing Datasets](dataset-viewer.md) — read a Parquet file without downloading it at all.
- [Core Concepts](../concepts.md) — why the bucket is laid out by content address.
