# Quickstart

This page takes you from nothing to a running instance with one dataset in it, visible in the web
UI and loadable from Python. It should take about fifteen minutes, most of which is the first
container build.

You need Docker with the Compose plugin, and Python 3.9 or later if you want to follow the upload
step in Python.

## 1. Start the server

The compose stack builds the API and web images from source, so start from a checkout of the
repository:

```bash
git clone https://github.com/dotneet/thinkingface.git
cd thinkingface
cp .env.example .env
docker compose up -d
```

That brings up four services: the web UI, the API, PostgreSQL for metadata, and a local Google
Cloud Storage emulator standing in for a real bucket. The first run builds images and takes a few
minutes; after that it starts in seconds.

| What | Where |
|---|---|
| Web UI | <http://localhost:3000> |
| API endpoint | <http://localhost:8080> |
| Default login | `admin` / `admin` |

Watch it come up with `docker compose logs -f`, and stop it later with `docker compose down`
(your data stays in named volumes).

!!! warning "Change the defaults before anyone else can reach this"

    `TF_ADMIN_PASSWORD` and `TF_SESSION_SECRET` in `.env` ship with development values. They are
    fine on your laptop and unacceptable anywhere else — over `https://`, the server refuses to
    start until you change them. See [Configuration](self-hosting/configuration.md).

## 2. Create an access token

Open <http://localhost:3000> and log in as `admin`. Go to **Settings → Access tokens**
(<http://localhost:3000/settings/tokens>), give the token a name, choose the **Write** scope, and
create it.

The value looks like `tf_xxxxxxxxxxxx`. Copy it now — it is shown once and never again.

One token covers everything: it is the password for git over HTTP, the `Authorization: Bearer`
value for the API, and the `HF_TOKEN` for the Python clients. Details in
[Authentication](reference/authentication.md).

## 3. Upload a dataset

Install the Hugging Face client libraries and point them at your instance:

```bash
pip install huggingface_hub datasets
export HF_ENDPOINT=http://localhost:8080
export HF_TOKEN=tf_xxxxxxxxxxxx
export HF_HUB_DISABLE_XET=1
```

If you have no dataset handy, write a tiny Parquet file to upload:

```python
import pyarrow as pa
import pyarrow.parquet as pq

table = pa.table({
    "text": ["この映画は最高だった", "退屈で途中で寝てしまった", "また観たい"],
    "label": [1, 0, 1],
})
pq.write_table(table, "train.parquet")
```

Now create the repository and push the file. This is ordinary `huggingface_hub` code — the only
thing that changed is where it points:

```python
from huggingface_hub import HfApi, upload_file

api = HfApi()
api.create_repo("admin/imdb-reviews", repo_type="dataset", exist_ok=True)

upload_file(
    path_or_fileobj="train.parquet",
    path_in_repo="data/train.parquet",
    repo_id="admin/imdb-reviews",
    repo_type="dataset",
)
```

`*.parquet` is routed through Git LFS by the `.gitattributes` the server writes into every new
repository, so the file's bytes go to object storage and git records a pointer. You do not have
to configure anything for that to happen.

!!! note "Why `HF_HUB_DISABLE_XET=1`?"

    Since `huggingface_hub` 1.0, large files are transferred over the Xet protocol whenever the
    `hf_xet` package is installed. thinkingface transfers over Git LFS and does not support Xet,
    so it has to be turned off. If you forget, the server returns an error saying exactly this.

## 4. Look at it in the web UI

Open <http://localhost:3000/datasets/admin/imdb-reviews>. The page opens on the repository **Card** —
the rendered `README.md` — with the size, file count, license and branches in the sidebar. Switch
to **Files** and you will see `data/train.parquet` carrying an `LFS` badge. Open it and choose
**Open in viewer** to browse its rows and schema as a table instead of downloading it.

Two other things worth clicking while you are here:

- **GCS access** on the repository page generates a `gcloud storage cp` script that lays this
  revision out under a destination of your choice — see [Downloading Files](guides/downloading.md).
- **History**, above the file list, shows the commit your upload created. There is only one write
  path in thinkingface — git — so uploads from Python, the CLI, git and the browser all land here
  the same way.

## 5. Read it back

The download side is symmetric — the same environment variables, the same functions:

```python
from huggingface_hub import hf_hub_download
from datasets import load_dataset

path = hf_hub_download(
    repo_id="admin/imdb-reviews", repo_type="dataset", filename="data/train.parquet"
)
ds = load_dataset("parquet", data_files=path)
```

Because the file sits under `data/` with a `train` prefix, `datasets` also recognises the split
layout, so `load_dataset("admin/imdb-reviews")` resolves the repository directly.

## Other ways to upload

Python is not the only path, and for a directory of files it is rarely the shortest one:

- **`tf up ./imdb-ja`** registers a whole directory in a single command — creating the repository,
  inferring whether it is a dataset or a model, and pushing everything as one commit. See the
  [tf CLI reference](reference/tf-cli.md).
- **`git clone` / `git push`** works exactly as you would expect, over HTTP or SSH, with Git LFS
  for the large files. See [Working with Git](guides/git.md).
- **The web UI** can create a repository at <http://localhost:3000/new> and edit Markdown or text
  files in the browser.

All of them are covered side by side in [Uploading Files](guides/uploading.md).

## Where to go next

- [Core Concepts](concepts.md) — repositories, revisions, how the bytes are actually stored, and
  why that layout lets you read the bucket directly.
- [Viewing Datasets](guides/dataset-viewer.md) and [Inspecting Models](guides/model-checkpoints.md)
  — what the browser can show you without downloading anything.
- [Tracking Experiments](guides/experiments.md) — point a training script at your instance and
  watch the metrics chart fill in.
- [Organizations](guides/organizations.md) — move from the `admin` namespace to a shared team one.
- [Deployment](self-hosting/deployment.md) — when the laptop instance needs to become a real one.
