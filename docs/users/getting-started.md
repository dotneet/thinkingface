# Getting Started

This page walks you from nothing to a dataset you can `load_dataset()` from, running on
your own machine.

## 1. Start the server

You need Docker with the Compose plugin. From a checkout of the repository:

```bash
cp .env.example .env
docker compose up -d
```

That brings up the web UI, the API, PostgreSQL, and a local GCS emulator.

- **Web UI** — <http://localhost:3000>
- **API** — <http://localhost:8080>
- **Default login** — `admin` / `admin`

Change the default credentials with `TF_ADMIN_USERNAME` / `TF_ADMIN_PASSWORD` in `.env`
before you expose the instance to anyone else.

To stop everything, run `docker compose down`.

## 2. Issue an access token

Log in to the web UI, then go to **Settings → Tokens** (<http://localhost:3000/settings/tokens>)
and create a token. It looks like `tf_xxxxxxxxxxxx`. Copy it now — the value is shown only
once.

## 3. Upload something

Pick whichever fits how you already work.

### From Python

Point `huggingface_hub` at your instance and use it exactly as you would against hf.co:

```bash
export HF_ENDPOINT=http://localhost:8080
export HF_TOKEN=tf_xxxxxxxxxxxx
export HF_HUB_DISABLE_XET=1
```

```python
from huggingface_hub import HfApi, upload_file, hf_hub_download
from datasets import load_dataset

api = HfApi()
api.create_repo("admin/imdb-ja", repo_type="dataset", exist_ok=True)

upload_file(
    path_or_fileobj="train.parquet",
    path_in_repo="data/train.parquet",
    repo_id="admin/imdb-ja",
    repo_type="dataset",
)

path = hf_hub_download(
    repo_id="admin/imdb-ja", repo_type="dataset", filename="data/train.parquet"
)
ds = load_dataset("parquet", data_files=path)
```

!!! note "Why `HF_HUB_DISABLE_XET=1`?"

    Since `huggingface_hub` 1.0, large files are transferred over the Xet protocol whenever
    the `hf_xet` package is installed. thinkingface transfers over Git LFS and does not
    support Xet, so it has to be turned off. If you forget, the server returns an error
    explaining exactly this.

### From the terminal

The `tf` CLI registers a whole directory in one command. It creates the repository if it
does not exist, infers whether it is a dataset or a model from the contents, and names it
after the directory:

```bash
tf login http://localhost:8080
tf up ./imdb-ja
```

### From git

A repository is a normal git remote, so `git clone` and `git push` work — including Git
LFS for large files.

## 4. Look at it in the web UI

Open the repository page. Parquet files get a browsable table view, model checkpoints get
a metadata panel, and Markdown or text files can be edited and committed straight from the
browser.

## Where to go next

- Group your work under a team namespace by creating an organization at
  <http://localhost:3000/orgs/new>.
- Track training runs with the trackio-compatible client and watch the metrics chart live.
