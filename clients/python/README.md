# thinkingface (Python client)

Small convenience layer on top of `huggingface_hub` for talking to a
self-hosted [thinkingface](../../README.md) instance, plus a
`trackio`-compatible shim for real-time experiment logging.

## Install

```bash
pip install -e clients/python
# or, once published:
pip install thinkingface
```

## `huggingface_hub` / `datasets`

thinkingface implements the subset of the HF Hub HTTP API that
`huggingface_hub` and `datasets` actually call, so pointing `HF_ENDPOINT` at
your server is enough — no code changes beyond that.

```python
import thinkingface

thinkingface.login("http://localhost:8080", token="tf_xxxxxxxxxxxx")

from huggingface_hub import HfApi, upload_file, hf_hub_download

api = HfApi()
api.create_repo("me/my-dataset", repo_type="dataset", exist_ok=True)

upload_file(
    path_or_fileobj="README.md",
    path_in_repo="README.md",
    repo_id="me/my-dataset",
    repo_type="dataset",
)

path = hf_hub_download(repo_id="me/my-dataset", repo_type="dataset", filename="README.md")
print(open(path).read())
```

`thinkingface.login()` just sets `HF_ENDPOINT`/`HF_TOKEN` (and calls
`huggingface_hub.login()` for you); you can do the same by hand. Call it
**before** importing anything from `huggingface_hub`, as above:
`huggingface_hub` resolves its default endpoint once, at import time, so
calling `login()` afterwards can leave it still talking to huggingface.co. If
`huggingface_hub` was already imported, `login()` does its best to retarget
it and raises rather than risk sending your token to the wrong server if it
can't.

```python
import os

os.environ["HF_ENDPOINT"] = "http://localhost:8080"
os.environ["HF_TOKEN"] = "tf_xxxxxxxxxxxx"
```

## `datasets`

```python
from datasets import load_dataset

ds = load_dataset(
    "parquet",
    data_files=hf_hub_download(
        repo_id="me/my-dataset",
        repo_type="dataset",
        filename="data/train.parquet",
    ),
)
```

## trackio (real-time logging)

`thinkingface.trackio` mirrors `trackio`'s `init` / `log` / `finish` API but
streams points directly to thinkingface's ingest endpoint instead of
buffering to local SQLite. Metrics still land in the same place trackio's
own batch sync would write them (a Parquet file inside a dataset repo), so
both approaches are interchangeable and inspectable with DuckDB or
`gcloud storage cp`.

```python
import os

os.environ["THINKINGFACE_ENDPOINT"] = "http://localhost:8080"
os.environ["THINKINGFACE_TOKEN"] = "tf_xxxxxxxxxxxx"
# Optional: defaults to "{your username}/trackio-metrics"
os.environ["THINKINGFACE_REPO"] = "me/trackio-metrics"

from thinkingface import trackio

run = trackio.init(project="mnist", name="baseline", config={"lr": 1e-3, "epochs": 10})
for step in range(100):
    trackio.log({"loss": 1.0 / (step + 1), "accuracy": step / 100}, step=step)
trackio.finish()
```

Points are buffered and flushed every 5 seconds or every 100 points,
whichever comes first, and always flushed on process exit (`atexit`).
Network errors are logged as warnings and never raised — a flaky
connection to the server will not abort your training run.

A metric whose value is `NaN` or `±inf` is **dropped** (with one warning per
run) and the rest of the point is sent as usual: JSON has no way to spell
those values, so a point carrying one could never be delivered. A diverging
loss therefore shows up as a gap in the chart rather than stopping the run's
logging.

| Environment variable | Default | Meaning |
| --- | --- | --- |
| `THINKINGFACE_ENDPOINT` | `http://localhost:8080` | server URL |
| `THINKINGFACE_TOKEN` | — | access token (write scope) |
| `THINKINGFACE_REPO` | `{user}/trackio-metrics` | target dataset repository |
| `THINKINGFACE_MODE` | `online` | `offline` writes the run to disk instead (below) |
| `THINKINGFACE_OFFLINE_DIR` | `./thinkingface-offline` | where offline runs and spilled points go |
| `THINKINGFACE_HEARTBEAT_SECS` | `30` | liveness interval (max 3600, `0` = off) |
| `THINKINGFACE_ARTIFACT_INTERVAL` | `60` | seconds between background artifact commits (`0` = only `save()` / `finish()`) |
| `THINKINGFACE_META` / `THINKINGFACE_SYSTEM_METRICS` | on | `off` disables env metadata / system telemetry |

### Config values

`config` may be a dict, an `argparse.Namespace` or a dataclass instance —
`trackio.init(project="mnist", config=parser.parse_args())` works as is.
Values JSON cannot carry are converted when they are sent instead of costing
the run its whole config:

| Value | Stored as |
| --- | --- |
| `pathlib.Path` | its string |
| `enum.Enum` | its `.value` (its name if the value is not encodable) |
| dataclass / `argparse.Namespace` | a nested object of its fields |
| numpy scalar / array | a number / a list (arrays over 1000 elements: a short `ndarray(shape=…, dtype=…)` string) |
| `datetime` / `date` | ISO 8601 |
| `set` / `tuple` | a list |
| non-string dict keys | their `str()` |
| `NaN` / `inf` / `-inf` | the strings `"nan"` / `"inf"` / `"-inf"` |
| anything else | `str(value)`, with one warning per run naming the keys |

`run.config` itself keeps your original objects; only what is sent is
converted. numpy is never imported by the client.

### Heartbeats

Every batch tells the server how often to expect news from the run
(`heartbeat_secs`, 30 by default), and a run that has sent nothing for that
long — a slow evaluation, a long checkpoint write — posts an empty batch as a
liveness ping. The server uses it to mark a run whose process died as
`stale` within a couple of minutes instead of half an hour, without flagging
a run that is merely quiet. Set `THINKINGFACE_HEARTBEAT_SECS=0` to turn both
off.

### Artifacts

`trackio.log_artifact(path, name=None)` attaches a file — or a whole
directory — to the current run. There is no artifact store: the file is
committed to the run's own dataset repository under
`{project}/artifacts/{run}/{name}`, through the same upload path
`huggingface_hub` uses, so it is git-versioned, comes down with
`git clone`, is readable straight out of the bucket at its content-addressed
key with `gcloud storage cp` (see `GET /api/v1/repos/{kind}/{ns}/{name}/gcs/{rev}`),
and goes over LFS automatically once it is big enough for the repository's
`.gitattributes`.

```python
trackio.log_artifact("out/confusion_matrix.png")  # → {project}/artifacts/{run}/confusion_matrix.png
trackio.log_artifact("out/eval.json", name="eval/raw.json")  # → .../artifacts/{run}/eval/raw.json
trackio.log_artifact("out/samples/")  # the whole directory, layout preserved
```

Artifacts are committed in batches while the run is going: everything
staged is committed together in the background every
`THINKINGFACE_ARTIFACT_INTERVAL` seconds (60 by default) when anything is
pending, `trackio.save()` commits right away, and `finish()` commits the
rest. A run that saves twenty plots a minute therefore makes one commit a
minute, and a crash no longer loses every artifact the run ever logged. A
path that does not exist, a name containing `..`, or the reserved name
`metrics.parquet` (the server reads a file with that name as an experiment's
metrics table) is a warning, never an exception.

### Images and tables

`trackio.Image` and `trackio.Table` can be logged like any metric value. They
are not metrics: each is written to a file and committed as an artifact of
the run, named after its key and step, and the point itself carries no value
for that key.

```python
trackio.log({"samples": trackio.Image("out/grid.png"), "loss": 0.4}, step=100)
# → {project}/artifacts/{run}/media/samples/step_00000100.png

trackio.log({"preds": trackio.Table(columns=["text", "label"], data=rows)}, step=100)
# → {project}/artifacts/{run}/tables/preds/step_00000100.parquet
```

- `Image(value, caption=None)` takes a path to an image file, a PIL image, or
  an HxW / HxWxC numpy array (uint8, or floats in `[0, 1]`). A PNG file is
  committed as is; anything else is encoded with Pillow, which is optional —
  without it, arrays and PIL images are skipped with one warning, and a
  non-PNG file is committed in its own format.
- `Table(dataframe=None, columns=None, data=None)` takes a pandas DataFrame,
  a `pyarrow.Table`, or rows (lists matching `columns`, or dicts). It is
  written as parquet with pyarrow (or pandas), so the repository's parquet
  viewer and SQL console can open it.

### Linking the model a run produced

`trackio.log_model("ns/name", revision=None)` records that this run built
that model. With no `revision` the model repository's current HEAD is
resolved, which is what you want right after pushing it:

```python
api.upload_folder(repo_id="me/bert-ja", folder_path="out/checkpoint")
trackio.log_model("me/bert-ja")  # pins whatever that push produced
```

The link is stored as a run *annotation*, not in `config` and not in any
README, so re-indexing the project cannot lose it and no card has to be
edited by hand. It shows up on both ends: the run page links to the model,
and the model's lineage view lists the run under "produced by run". A model
that is not on the server (a typo, or a push that never happened) is still
recorded and shown with a warning rather than dropped.

### Automatic run environment metadata

`trackio.init()` automatically collects a snapshot of the run's
environment and merges it into `config` under the reserved `_meta` key —
**this data is sent to your thinkingface server and stored with the run**,
the same as anything else in `config`:

- `_meta.git.commit` / `_meta.git.branch` / `_meta.git.dirty` — state of the
  git repo the script is running from (silently omitted if `git` isn't
  installed or the script isn't inside a repo)
- `_meta.cmdline` — `sys.argv`, with the values of flags that look like
  secrets (`--token`, `--password`, `--api-key`, `--secret`, `--auth`,
  `--credential`, and variants of these) replaced with `***`
- `_meta.python` / `_meta.platform` / `_meta.hostname`
- `_meta.gpu.name` / `_meta.gpu.count` / `_meta.cuda` — read via `torch` if
  installed, else `nvidia-smi`; omitted if neither is available
- `_meta.requirements_sha256` — a SHA-256 hash of the sorted list of
  installed package name/version pairs (a "pip freeze equivalent"), so two
  runs can be compared for "same environment or not" without storing the
  full package list

Collection is entirely best-effort: any individual item that can't be
determined (no git, no GPU, etc.) is silently dropped, and `init()` never
raises because of the environment it happens to run in. `_meta` is a
reserved config key — avoid using it for your own config values.

Set `THINKINGFACE_META=off` to disable this collection entirely:

```python
os.environ["THINKINGFACE_META"] = "off"
```

### Resuming an interrupted run

On a Spot / preemptible VM a long training job is interrupted and restarted
as a matter of course. `resume=` decides what happens when the project
already has a run with the name you passed:

| `resume=` | behaviour |
| --- | --- |
| `"never"` (default) | Never write into an existing run. A name that is already taken gets a `-1` / `-2` / … suffix and a warning, so the restart logs its own curve instead of interleaving itself into the first one. |
| `"allow"` (or `True`) | Continue the existing run if there is one, otherwise start it. |
| `"must"` | Continue the existing run, and raise `RuntimeError` if it does not exist (or cannot be looked up). |

```python
run = trackio.init(project="mnist", name="baseline", resume="allow", config={"lr": 1e-3})
for step in range(run.step, 100_000):
    trackio.log({"loss": ...})
```

Continuing a run means:

- **Steps continue.** `init()` reads the run's `last_step` from the server
  and starts at `last_step + 1`, so the chart is one line rather than two
  overlapping ones. `run.step` is that starting step, which is what the loop
  above resumes from. (Online only: an offline run cannot ask — see below.)
- **Status goes back to `running`.** A run that was already marked
  `finished` (or `failed`) reverts on the first flush.
- **Configs are merged.** Keys only the previous attempt set survive; on a
  conflict the value from the code that is running now wins, and the
  differences are recorded under the reserved `_resume` config key
  (`_resume.count`, `_resume.from_step`, `_resume.config_changes`) so a
  learning rate that silently changed between attempts is still visible on
  the run page.
- **A step logged twice wins the second time.** If the checkpoint you
  restarted from is a few steps behind, the re-computed values replace the
  ones the dead attempt left at those steps — the server keeps both in the
  Parquet, and the chart shows the later one.

`resume="never"` with an auto-generated name (no `name=`) makes no extra
request, so the default path works exactly as before with no server
reachable.

### Offline runs and `tf experiments sync`

With `THINKINGFACE_MODE=offline` (or `trackio.init(..., mode="offline")`)
the shim makes no network request at all — not even to look up your user —
and writes the run to a directory instead:

```
thinkingface-offline/20260927T101500-mnist-baseline-1a2b3c4d/
    run.jsonl     # init / log / artifact / model / finish records, one per line
    artifacts/    # copies of log_artifact files, media and tables
```

Everything behaves as online: points (including system metrics) are written
every 5 seconds or 100 points, `log_artifact` copies the file in right away,
`log_model` records the revision you pass (or none — there is no server to
ask), and `finish()` records the final status. Where the directory is, and
the command that uploads it, is printed to stderr when the run starts:

```bash
tf experiments sync thinkingface-offline/          # every run in it
tf experiments sync thinkingface-offline/ --watch  # keep following live runs
```

The repository is `THINKINGFACE_REPO` if set, otherwise
`{user}/trackio-metrics` resolved at sync time; `resume=` is honoured at sync
time too — with one exception: **offline, steps do not continue.** There is no
server to ask for the existing run's `last_step`, so auto-numbered steps start
at 0 and the sync does not shift them (a one-time warning says so). Pass
explicit `step=` if an offline run continues an existing one:

```python
run = trackio.init(project="mnist", name="baseline", resume="allow", mode="offline")
for step in range(checkpoint_step + 1, 100_000):
    trackio.log({"loss": ...}, step=step)
```

The **online** mode uses the same directories as a safety net. Points it
would otherwise have had to drop — the oldest ones when the in-memory retry
buffer overflows during a long outage, and whatever is still unsent when
`finish()` runs out of retries — are written to a run directory under
`THINKINGFACE_OFFLINE_DIR` instead (and so are artifacts `finish()` could not
commit), and the warning names the directory and the `tf experiments sync`
command that delivers them to the same run. Points the server *rejected*
(a 4xx: bad token, unknown repository, malformed data) are still dropped.
If the disk refuses too, the points are dropped with a warning as before —
the client never raises into your training script. A spilled record leaves
the config out once the server already has it, so syncing late points never
rolls back a newer config; and when `THINKINGFACE_REPO` was unset and your
user could not be looked up, the directory names no repository and the sync
resolves `{user}/trackio-metrics` itself.

### Drop-in replacement for `trackio`

If your training script already imports `trackio`, you can switch it to the
real-time thinkingface path with a single import alias:

```python
import thinkingface.trackio as trackio  # instead of `import trackio`
```

### Autolog for `transformers` / PyTorch Lightning

`thinkingface.trackio.integrations` provides ready-made autolog hooks for the
two training loops most thinkingface users are already on, so you don't have
to sprinkle `trackio.log(...)` through your own loop. Both `transformers` and
`lightning` (or the older `pytorch_lightning`) are **optional dependencies**:
importing `thinkingface.trackio.integrations` always works, even with
neither installed — only instantiating the corresponding class requires the
matching library (`pip install "thinkingface[transformers]"` /
`pip install "thinkingface[lightning]"`).

#### `transformers.Trainer`

`ThinkingFaceCallback` is a `TrainerCallback` that opens a run in
`on_train_begin`, forwards every `on_log` call as metrics (using
`state.global_step` as the step), and closes the run in `on_train_end`.
The `TrainingArguments` in effect are recorded under `config["_args"]`.

```python
from thinkingface.trackio.integrations import ThinkingFaceCallback
from transformers import Trainer, TrainingArguments

trainer = Trainer(
    model=model,
    args=TrainingArguments(output_dir="out", report_to=[]),
    callbacks=[ThinkingFaceCallback(project="mnist", config={"notes": "baseline"})],
    ...,
)
trainer.train()
```

#### PyTorch Lightning

`ThinkingFaceLightningLogger` implements Lightning's `Logger` interface
(`log_hyperparams` / `log_metrics` / `finalize`). The run is created lazily
on the first `log_hyperparams`/`log_metrics` call, so hyperparameters passed
to `Trainer.logger.log_hyperparams(...)` before training starts are folded
into the run's initial config.

```python
import lightning as pl
from thinkingface.trackio.integrations import ThinkingFaceLightningLogger

trainer = pl.Trainer(logger=ThinkingFaceLightningLogger(project="mnist"))
trainer.fit(model)
```

Both integrations reuse `trackio.init`/`log`/`finish` under the hood, so the
same buffering, retry, and automatic `_meta` environment-snapshot behavior
described above applies.
