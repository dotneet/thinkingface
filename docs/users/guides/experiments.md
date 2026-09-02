# Tracking Experiments

thinkingface records training runs the way trackio, Weights & Biases or MLflow do — projects,
runs, hyperparameters, metric series — and charts them in the web UI. This page covers how runs
get in, what you write in your training script, and what the UI gives back.

The important difference from a hosted tracker: there is no separate experiment database you have
to trust. Every run ends up as a Parquet file inside an ordinary dataset repository, so the data
is git-versioned, clonable, and readable with DuckDB or `gcloud storage` without going through
thinkingface at all.

## The data model

| Term | What it is |
|---|---|
| experiment repository | A dataset repository holding the Parquet that experiment data lives in. Conventionally `{you}/trackio-metrics`. |
| project | One tracked body of work inside that repository — usually one model or one task. |
| run | A single training attempt within a project. Has a name, a status, a config and metrics. |
| config | The run's hyperparameters, a JSON object, recorded once when the run starts. |
| metric series | The `(step, value)` points logged for one metric name in one run. |
| summary | The last value seen for each metric, shown in the run list and on the run page. |

A run's status is `running`, `finished` or `failed`. Nothing else is stored.

A dataset repository is treated as an experiment repository when any of these is true:

- it contains a `metrics.parquet` (at the repository root or in any directory), or
- its README card carries a `trackio` or `experiment` tag, or
- its README card sets `thinkingface_experiment: true`.

Flagged repositories get an **Experiments** tab on the repository page, and appear in the
**Experiments** section of the top navigation.

## Two ways to get runs in

Both paths write to the same place. Pick per project; you do not have to pick for the whole
instance.

| Aspect | Batch sync (route A) | Real-time ingest (route B) |
|---|---|---|
| What you import | `trackio` itself | `thinkingface.trackio` |
| Code changes | None — only `HF_ENDPOINT` | One import line |
| How data arrives | trackio's own Parquet sync pushes to the dataset repository | Points POST to the server, buffered, then flushed to the same Parquet |
| Chart latency | Whatever trackio's sync interval is | Seconds |
| Where the truth lives | Parquet in the dataset repository | Parquet in the dataset repository |

Use route A when you already run trackio and do not want to touch the script. Use route B when
you want to watch a curve while the job is running.

### Route A — trackio's Parquet sync

Point trackio's Hugging Face client at your instance and let its dataset sync run as it normally
does:

```bash
export HF_ENDPOINT=http://localhost:8080
export HF_TOKEN=tf_xxxxxxxxxxxx
export HF_HUB_DISABLE_XET=1
```

Every push to the dataset repository triggers indexing, which scans the Parquet files and rebuilds
the run index. The layouts that are recognised are:

```text
metrics.parquet              + aux/configs.parquet          -> project named after the repository
{project}/metrics.parquet    + {project}/aux/configs.parquet
{project}.parquet            + {project}_configs.parquet
```

`{project}_system.parquet` is picked up as machine telemetry for that project, but never creates a
project of its own — a project without a metrics file is not a project. The reader looks for a run
column named `run_name`, `run` or `run_id`, a step column named `step`, `_step` or `global_step`,
and a timestamp column named `timestamp`, `_timestamp` or `created_at`. Every other column is
treated as a metric.

### Route B — the `thinkingface.trackio` shim

The `thinkingface` Python package ships a shim with the same `init` / `log` / `finish` surface as
trackio (and, by extension, wandb), which posts points straight to the server instead of buffering
to local SQLite.

Install it from a checkout of the repository:

```bash
pip install -e clients/python
```

Configure it with environment variables:

| Variable | Meaning |
|---|---|
| `THINKINGFACE_ENDPOINT` | Base URL of the server. Defaults to `http://localhost:8080`. |
| `THINKINGFACE_TOKEN` | Your access token (`tf_...`). Needs write scope. |
| `THINKINGFACE_REPO` | Target dataset repository, `namespace/name`. Defaults to `{your username}/trackio-metrics`. |
| `THINKINGFACE_META` | Set to `off` to skip the automatic environment snapshot. |
| `THINKINGFACE_SYSTEM_METRICS` | Set to `off` to skip GPU/CPU/memory telemetry. |

!!! warning

    The target dataset repository must already exist. Ingest writes into a repository you have
    write access to; it does not create one for you. Create it once with
    `HfApi().create_repo("admin/trackio-metrics", repo_type="dataset", exist_ok=True)` or from
    the web UI.

## Log metrics from a training loop

A complete, working script:

```python
import os

os.environ["THINKINGFACE_ENDPOINT"] = "http://localhost:8080"
os.environ["THINKINGFACE_TOKEN"] = "tf_xxxxxxxxxxxx"
os.environ["THINKINGFACE_REPO"] = "admin/trackio-metrics"

from thinkingface import trackio

run = trackio.init(
    project="sentiment-finetune",
    name="baseline",
    config={"lr": 3e-5, "batch_size": 32, "epochs": 3},
)

for step, batch in enumerate(loader):
    loss = train_step(batch)
    trackio.log({"train/loss": loss}, step=step)

    if step % 500 == 0:
        trackio.log({"eval/accuracy": evaluate(model)}, step=step)

trackio.finish()
```

`log()` takes a dict of metric name to number, plus an optional `step`. Omitting `step` advances
the run's own counter by one. A metric name may be anything without control characters, up to 256
bytes; slashes are conventional for grouping (`train/loss`, `eval/accuracy`) and are fine.

Points are buffered in the process and flushed every 5 seconds or every 100 points, whichever
comes first, and always on process exit. **A network failure never raises into your training
loop** — it is reported as a warning and the points are kept for the next attempt. The one
exception is `resume="must"` (below), which cannot be honoured without reaching the server.

The server-side ingest API this shim talks to has its own limits, which matter mostly if you
are logging unusually large or unusually varied batches: a single ingest request may carry at
most 10,000 points, and a run may carry at most 1,000 distinct metric names over its lifetime
(every metric name a run has ever logged is kept, so this is a lifetime count, not a per-batch
one). Neither is something the normal `log()` usage above comes close to.

If your script already imports `trackio`, switching to the real-time path is one line:

```python
import thinkingface.trackio as trackio  # instead of `import trackio`
```

### Resume an interrupted run

On a preemptible VM a job gets killed and restarted as a matter of course. `resume=` decides what
happens when the project already has a run with the name you passed:

| `resume=` | Behaviour |
|---|---|
| `"never"` (default) | Never writes into an existing run. A taken name gets a `-1` / `-2` suffix and a warning, so the restart logs its own curve. |
| `"allow"` (or `True`) | Continues the existing run if there is one, otherwise starts it. |
| `"must"` | Continues the existing run, and raises `RuntimeError` if it does not exist. |

```python
run = trackio.init(project="sentiment-finetune", name="baseline", resume="allow",
                   config={"lr": 3e-5})

for step in range(run.step, 100_000):
    trackio.log({"train/loss": train_step()}, step=step)
```

Continuing a run means the step counter picks up from the server's recorded `last_step + 1`
(exposed as `run.step`, which is what the loop above starts from), the status goes back to
`running` on the first flush, and the two configs are merged — keys only the previous attempt set
survive, a conflicting value from the running code wins, and the differences are recorded under
the reserved `_resume` config key so a learning rate that changed between attempts stays visible.

If the checkpoint you restarted from is a few steps behind, the recomputed values replace the dead
attempt's at those steps on the chart. Both are kept in the Parquet; the chart draws the later
one.

### Group runs into a sweep

`group=` names the sweep a run belongs to and `job_type=` the role it plays in it, spelled the way
wandb spells them:

```python
trackio.init(project="sentiment-finetune", name=f"lr-{lr}", group="lr-sweep",
             job_type="train", config={"lr": lr})
```

Runs sharing a group collapse into one foldable row in the run table and can be compared
axis-by-axis in the parallel-coordinates view. A run without a group is listed flat.

`trackio.init()` also accepts and ignores any other keyword argument, so a call site written
against wandb or upstream trackio (`tags=`, for instance) doesn't raise just because this shim
doesn't support that option — but each ignored argument gets its own warning naming it, so an
option you expected to take effect doesn't silently do nothing.

### Attach artifacts to a run

`trackio.log_artifact(path, name=None)` attaches a file — or a whole directory — to the current
run:

```python
trackio.log_artifact("out/confusion_matrix.png")             # -> {project}/artifacts/{run}/confusion_matrix.png
trackio.log_artifact("out/eval.json", name="eval/raw.json")  # -> .../artifacts/{run}/eval/raw.json
trackio.log_artifact("out/samples/")                         # the whole directory, layout preserved
```

There is no separate artifact store. The files are committed into the run's own dataset
repository under `{project}/artifacts/{run}/`, through the same upload path `huggingface_hub`
uses — so they are git-versioned, come down with `git clone`, and go over LFS automatically once
they are large enough for the repository's `.gitattributes`. See
[Downloading Files](downloading.md) for how to get them back out.

Nothing is uploaded while the run is going: everything is committed together when `finish()` runs,
so a run that saves twenty plots makes one commit rather than twenty. A path that does not exist,
a name containing `..`, or the reserved name `metrics.parquet` produces a warning, never an
exception.

A directory attached this way has limits, and blowing past one of them means **none** of that
call's files upload, not a truncated subset:

- **500 files per `log_artifact()` call.** Past that, the call warns and stages nothing at all —
  it's an all-or-nothing refusal, not a truncation. Log the files you need individually, or push
  a model repository instead of attaching a large checkpoint directory this way.
- **A symlink to a file is followed and uploaded like any other file.** A symlink to a directory
  is not descended into, and a dangling symlink resolves to neither — both are skipped, with a
  warning naming them.
- **An empty directory is a warning, not an empty no-op upload** — there is nothing to stage, so
  the call fails the same way a directory over the 500-file limit does.

### Link the model a run produced

`trackio.log_model("ns/name", revision=None)` records that this run built that model. With no
`revision`, the model repository's current HEAD is resolved — which is what you want immediately
after pushing it:

```python
api.upload_folder(repo_id="acme/sentiment-base", folder_path="out/checkpoint")
trackio.log_model("acme/sentiment-base")
```

The link is stored as a run annotation rather than as a config value or a README edit, so
re-indexing the project cannot lose it. It shows up on both ends: the run page lists the model
under **Models produced**, and the model's lineage view links back to the run. A model that does
not exist on the server is still recorded, and shown with a warning rather than dropped.

### Automatic environment snapshot

`trackio.init()` collects a best-effort snapshot of the run's environment and merges it into
`config` under the reserved `_meta` key. **This is sent to your server and stored with the run**,
the same as anything else in `config`:

- `_meta.git.commit` / `.branch` / `.dirty` — state of the git repository the script runs from
- `_meta.cmdline` — `sys.argv`, with values of secret-looking flags (`--token`, `--password`,
  `--api-key`, `--secret`, `--auth`, `--credential` and variants) replaced with `***`
- `_meta.python` / `_meta.platform` / `_meta.hostname`
- `_meta.gpu.name` / `.count` / `.cuda` — read via `torch` if installed, else `nvidia-smi`
- `_meta.requirements_sha256` — a hash of the sorted installed package name/version pairs, so two
  runs can be compared for "same environment or not" without storing the full list

Anything that cannot be determined is silently dropped, and `init()` never raises because of it.
The run page renders this under **Run environment**. Set `THINKINGFACE_META=off` to turn the whole
collection off. `_meta` is a reserved config key — do not use it for your own values.

### System metrics

Every active run also samples GPU, CPU and memory usage roughly every 10 seconds and logs it under
`system/`-prefixed keys (`system/gpu.0.util`, `system/cpu.percent`, and so on). These get their own
**System metrics** tab in the chart area, so they never crowd out the metrics your script logs.

Telemetry is best-effort: a machine with no GPU and no `psutil` simply logs nothing. Set
`THINKINGFACE_SYSTEM_METRICS=off` to disable it.

Whether it counts toward the run's point count and last step depends on which route logged it
(see [Two ways to get runs in](#two-ways-to-get-runs-in) above):

- **Route A** (trackio writing its own Parquet, indexed by the server) keeps system telemetry
  entirely out of `num_points`, `last_step` and the run's start time — it's sampled on a
  wall-clock timer of its own, so counting it would make "how many points did this run log" and
  "what step is it on" depend on how long the machine happened to be up.
- **Route B** (the `thinkingface.trackio` shim) does not make that distinction: a system-metric
  sample goes through the same buffer and the same ingest request as any other logged point, so
  it counts toward `num_points` exactly like a metric you logged yourself (it's logged at the
  *current* step without advancing it, so it rarely moves `last_step` on its own). Each
  `system/`-prefixed key also counts toward the 1,000-distinct-metric-names ceiling mentioned
  above, though that set is small and fixed, so it never gets close to it by itself.

### Framework integrations

`thinkingface.trackio.integrations` provides autolog hooks for two training loops, so you do not
have to sprinkle `trackio.log(...)` through code you did not write. Both underlying libraries are
optional dependencies — importing the module works with neither installed; only instantiating the
class needs the matching library.

For `transformers.Trainer`, `ThinkingFaceCallback` opens a run at `on_train_begin`, forwards every
`on_log` call as metrics using `state.global_step`, closes the run at `on_train_end`, and records
the `TrainingArguments` under `config["_args"]`:

```python
from thinkingface.trackio.integrations import ThinkingFaceCallback
from transformers import Trainer, TrainingArguments

trainer = Trainer(
    model=model,
    args=TrainingArguments(output_dir="out", report_to=[]),
    callbacks=[ThinkingFaceCallback(project="sentiment-finetune", config={"notes": "baseline"})],
)
trainer.train()
```

For PyTorch Lightning, `ThinkingFaceLightningLogger` implements Lightning's `Logger` interface.
The run is created lazily on the first `log_hyperparams` / `log_metrics` call, so hyperparameters
passed before training starts are folded into the run's initial config:

```python
import lightning as pl
from thinkingface.trackio.integrations import ThinkingFaceLightningLogger

trainer = pl.Trainer(logger=ThinkingFaceLightningLogger(project="sentiment-finetune"))
trainer.fit(model)
```

Install the extras with `pip install "thinkingface[transformers]"` or
`pip install "thinkingface[lightning]"`.

## Explore runs in the web UI

**Experiments** in the top navigation lists every experiment repository, with a search box and a
project count. Opening one lists its projects; opening a project opens the dashboard.

![The run list for a project, showing run names, status, last step, metric columns and tags](../images/experiment-runs.png)

The run table shows each run's name, status, tags, last step, its summary metrics as columns, when
it started, and any checkpoints it produced. Columns sort, groups fold into a single row, and a
metric filter narrows the list to runs matching a threshold (for example `eval/accuracy > 0.9`).
The first five runs are selected when the page opens; the checkboxes control what the views below
plot. An **Export table CSV** button downloads the table as-is — a folded group still exports
every member, not just its header row, so the file always matches what the current filters
selected rather than what happens to be visible. The Metrics view (below) has its own
**Export metrics CSV** button, on both the project dashboard and a single run's page.

![Metric charts overlaying several runs, with step and time axes and a smoothing control](../images/experiment-charts.png)

Four views sit under the table:

- **Metrics** — one chart per metric name, with every selected run overlaid. The X axis switches
  between step and wall-clock time, smoothing and a log scale are available, and zoom can be
  synchronised across all charts. System metrics get their own tab.
- **Config diff** — a table of hyperparameters across the selected runs, with a "differences only"
  toggle. `_meta` and `_args` are excluded unless you ask for them.
- **Scatter** — any numeric hyperparameter or metric against any other.
- **Parallel** — parallel coordinates across the selected runs, for reading a sweep axis by axis.
  Text hyperparameters are spaced evenly along their axis.

### The run page

Clicking a run opens its own page, which carries, in order: a summary of the last value of each
metric, that run's charts, its artifacts, the models it produced, a free-form Markdown note, its
hyperparameters, the `TrainingArguments` if a Trainer logged them, and the environment snapshot.

### Annotate and clean up runs

These need write access to the backing dataset repository, and are shared state rather than a
per-viewer preference:

- **Tags** — free-form labels, up to 32 per run. The dashboard filters by them.
- **Baseline** — marks one run as the reference. The charts label it as such, so it is
  identifiable when several runs are overlaid.
- **Archive** — hides a run from the table without deleting anything. Reversible, and archived
  runs can be shown again with a checkbox.
- **Note** — Markdown prose about what the run was for and what it showed.
- **Delete** — removes the run and every metric point still held for it, irreversibly.

!!! warning

    Deleting a run does not rewrite git history. A run whose points came from a Parquet export
    reappears the next time that export is indexed — the export is that path's source of truth.
    Deleting the repository is the way to remove those for good.

## Where the data actually lives

Points from the real-time ingest API land in the database first, which is what makes the chart
live. That buffer is not the source of truth. A background worker polls every 10 seconds and
writes the buffer into the dataset repository's Parquet — after `TF_EXP_FLUSH_INTERVAL` has
elapsed (one minute by default), and **immediately when a run reaches `finished` or `failed`**.

The flush writes to the same file route A does: the `metrics.parquet` already detected for that
project, or `{project}/metrics.parquet` if there is none yet. Columns are `run_name`, `step`,
`timestamp`, plus one per metric. The commit is made server-side and signed as `thinkingface`,
with the message `chore(trackio): flush {project} metrics`, so `git log` makes it obvious nobody
typed it. Because `*.parquet` is LFS-tracked by default, the payload goes to object storage and an
LFS pointer is committed.

A newly created experiment repository therefore shows up in the Experiments list once its first
flush lands — within a minute for a live run, or right away if you gave its README card a
`trackio` tag.

The practical consequence: your experiment data comes down with `git clone`, can be read straight
out of the bucket with the `gcloud storage cp` script the repository page generates, and can be
queried with DuckDB without the server involved. See [Downloading Files](downloading.md) for both
routes.

Two points are also worth knowing about how charts read that file:

- During a flush the same point briefly exists in both git and the database. It is de-duplicated
  by an internal `_ingest_id` column, so the chart is never doubled and never missing a point, no
  matter when you look.
- Two genuinely different values logged at the same step — from a resume, or from logging a step
  twice — are both kept in the Parquet. The chart draws whichever was logged later.

A flush rebuilds the target `metrics.parquet` in memory (there's no way to append a row group to
an existing file today), so there's a ceiling on how large that file can grow and still be
flushed: 1,000,000 existing rows. This is a project-wide ceiling shared by every run writing to
that project's `metrics.parquet`, not a per-run one, and no realistic single training run
reaches it on its own — it matters mainly for a project accumulating a very long history of
runs.

Past that ceiling, **no data is lost.** The project's still-unflushed points stay buffered in
the database (the live chart keeps reading them from there, so nothing disappears from the UI
either) and a flush is retried automatically about once an hour, rather than writing a
truncated file or discarding the points that couldn't be written. The retry keeps failing until
the underlying condition changes — the file shrinking back under the ceiling, most likely by
deleting some of the runs that contributed to it — so this is something an operator needs to
notice and act on, not something that resolves itself. Today that means watching the server
logs for the error `experiment project cannot be flushed; its buffered points are being kept`,
which names the project; there is no dedicated CLI or UI surface for it yet.

## Related pages

- [Uploading Files](uploading.md) — pushing the dataset repository the runs live in
- [Downloading Files](downloading.md) — pulling the Parquet back out, and reading it from the bucket
- [Viewing Datasets](dataset-viewer.md) — browsing that Parquet as a table in the browser
- [Authentication](../reference/authentication.md) — issuing the write-scoped token ingest needs
- [Organizations](organizations.md) — sharing an experiment repository with a team
