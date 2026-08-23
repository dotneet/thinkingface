"""Route B's ingest buffer must end up as parquet in the dataset repository.

docs/thinkingface-design.md §8 offers two ways to record an experiment: the
trackio batch sync (route A) and the native ingest API (route B). Both are
promised the *same* storage: a Parquet file inside a thinkingface dataset
repository, so the data is git-versioned, readable straight out of the bucket
via its content-addressed `lfs/{oid}` key for `gcloud storage` / DuckDB /
BigQuery, and travels with a clone.

Route B accepts points into the database first so the dashboard can be live.
This test drives that path end to end and checks the promise is kept: log a
run through the ingest API, finish it, and confirm the sync worker's flush
turns the buffer into `{project}/metrics.parquet` -- readable as parquet,
indexed at its content-addressed `lfs/{oid}` key (surfaced through `GET
/api/v1/repos/{kind}/{ns}/{name}/gcs/{rev}`), and with the metrics endpoint
reporting exactly the same series before and after (no duplicated points,
none lost).

Requires a running server + fake-gcs-server; see e2e/README.md.
"""

from __future__ import annotations

import io
import os
import time
import urllib.parse

import pytest
import requests
from huggingface_hub import HfApi, hf_hub_download

GCS_EMULATOR_URL = os.environ.get("GCS_EMULATOR_URL", "http://localhost:4443").rstrip("/")
GCS_BUCKET = os.environ.get("GCS_BUCKET", "thinkingface")

# The flusher polls every 10s and flushes a finished run on the next poll, then
# re-indexes before the file becomes visible. 90s is slack for a loaded CI
# runner; the flush itself takes milliseconds.
FLUSH_TIMEOUT_SECONDS = 90
FLUSH_POLL_INTERVAL_SECONDS = 2

PROJECT = "demo"
RUN = "run-1"
STEPS = [1, 2, 3, 4, 5]


def _metrics_for(step: int) -> dict[str, float]:
    return {"loss": round(1.0 / step, 6), "accuracy": round(step / 10, 6)}


def _experiments_url(endpoint: str, namespace: str, repo: str, suffix: str) -> str:
    return (
        f"{endpoint}/api/v1/experiments/"
        f"{urllib.parse.quote(namespace)}/{urllib.parse.quote(repo)}/"
        f"{urllib.parse.quote(PROJECT)}/{suffix}"
    )


def _series(endpoint: str, token: str, namespace: str, repo: str) -> dict[tuple[str, str], list]:
    """Fetch the chart data the UI draws, keyed by (run, metric)."""
    resp = requests.get(
        _experiments_url(endpoint, namespace, repo, "metrics"),
        headers={"Authorization": f"Bearer {token}"},
        timeout=10,
    )
    resp.raise_for_status()
    return {(s["run"], s["key"]): s["points"] for s in resp.json()["series"]}


def _wait_for_gcs_entry(
    endpoint: str, token: str, namespace: str, repo: str, path: str, timeout: float
) -> dict:
    """Poll GET /api/v1/repos/dataset/{ns}/{repo}/gcs/main until `path` is
    indexed, and return its RepoGCSFile entry."""
    url = f"{endpoint}/api/v1/repos/dataset/{namespace}/{repo}/gcs/main"
    headers = {"Authorization": f"Bearer {token}"}
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        resp = requests.get(url, headers=headers, timeout=10)
        resp.raise_for_status()
        for f in resp.json()["files"]:
            if f["path"] == path:
                return f
        time.sleep(FLUSH_POLL_INTERVAL_SECONDS)
    pytest.fail(f"{path} never appeared in /gcs/main within {timeout}s")


def _wait_for_flush(hf_api: HfApi, repo_id: str, path: str) -> None:
    """Block until the flushed parquet shows up in the repository listing."""
    deadline = time.monotonic() + FLUSH_TIMEOUT_SECONDS
    seen: list[str] = []
    while time.monotonic() < deadline:
        seen = hf_api.list_repo_files(repo_id=repo_id, repo_type="dataset")
        if path in seen:
            return
        time.sleep(FLUSH_POLL_INTERVAL_SECONDS)
    pytest.fail(
        f"{path} never appeared in {repo_id} within {FLUSH_TIMEOUT_SECONDS}s; "
        f"repository holds {sorted(seen)!r}. Is the flush enabled "
        "(TF_EXP_FLUSH_INTERVAL > 0)?"
    )


def test_ingested_run_is_flushed_to_parquet(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, namespace: str, unique_name: str
) -> None:
    pyarrow_parquet = pytest.importorskip("pyarrow.parquet")

    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="dataset", exist_ok=True)
    headers = {"Authorization": f"Bearer {hf_token}"}

    log = requests.post(
        _experiments_url(hf_endpoint, namespace, unique_name, "log"),
        headers=headers,
        json={
            "run": RUN,
            "status": "running",
            "config": {"lr": 0.001},
            "points": [{"step": step, "metrics": _metrics_for(step)} for step in STEPS],
        },
        timeout=15,
    )
    log.raise_for_status()
    assert log.json()["accepted"] == len(STEPS)

    before = _series(hf_endpoint, hf_token, namespace, unique_name)
    assert set(before) == {(RUN, "loss"), (RUN, "accuracy")}
    assert all(len(points) == len(STEPS) for points in before.values())

    # Finishing the run is what makes the flush happen on the next poll rather
    # than at the end of the configured interval.
    finish = requests.post(
        _experiments_url(hf_endpoint, namespace, unique_name, "finish"),
        headers=headers,
        json={"run": RUN, "status": "finished"},
        timeout=15,
    )
    finish.raise_for_status()

    metrics_path = f"{PROJECT}/metrics.parquet"
    _wait_for_flush(hf_api, repo_id, metrics_path)

    # 1. The chart must be byte-for-byte the same once the points live in git.
    after = _series(hf_endpoint, hf_token, namespace, unique_name)
    assert after == before, "the flush changed the chart (duplicated or lost points)"

    # 2. The file must be a real parquet with route A's column layout.
    local = hf_hub_download(
        repo_id=repo_id, repo_type="dataset", filename=metrics_path, token=hf_token
    )
    table = pyarrow_parquet.read_table(local)
    columns = set(table.column_names)
    assert {"run_name", "step", "timestamp", "loss", "accuracy"} <= columns
    assert table.num_rows == len(STEPS)

    rows = table.to_pylist()
    assert {row["run_name"] for row in rows} == {RUN}
    assert sorted(row["step"] for row in rows) == STEPS
    by_step = {row["step"]: row for row in rows}
    for step in STEPS:
        assert by_step[step]["loss"] == pytest.approx(_metrics_for(step)["loss"])
        assert by_step[step]["accuracy"] == pytest.approx(_metrics_for(step)["accuracy"])

    # 3. And it must be indexed at its content-addressed lfs/ key, which is
    #    what `gcloud storage cp` / DuckDB / BigQuery read directly (the
    #    `/gcs/{rev}` endpoint is what hands out that URI).
    gcs_file = _wait_for_gcs_entry(
        hf_endpoint, hf_token, namespace, unique_name, metrics_path, FLUSH_TIMEOUT_SECONDS
    )
    assert gcs_file["lfs"] is True, "*.parquet is LFS-tracked by default"
    assert gcs_file["uri"].startswith(f"gs://{GCS_BUCKET}/lfs/")

    # The object's bytes must be the same parquet, so DuckDB reading straight
    # out of the bucket sees what the repository holds.
    object_name = gcs_file["uri"].removeprefix(f"gs://{GCS_BUCKET}/")
    exported = requests.get(
        f"{GCS_EMULATOR_URL}/storage/v1/b/{GCS_BUCKET}/o/"
        f"{urllib.parse.quote(object_name, safe='')}",
        params={"alt": "media"},
        timeout=30,
    )
    exported.raise_for_status()
    mirrored = pyarrow_parquet.read_table(io.BytesIO(exported.content))
    assert mirrored.num_rows == table.num_rows


def test_flush_keeps_appending_across_batches(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, namespace: str, unique_name: str
) -> None:
    """A second batch must extend the same file, not replace it."""
    pyarrow_parquet = pytest.importorskip("pyarrow.parquet")

    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="dataset", exist_ok=True)
    headers = {"Authorization": f"Bearer {hf_token}"}
    url = _experiments_url(hf_endpoint, namespace, unique_name, "log")

    def log(steps: list[int], status: str) -> None:
        resp = requests.post(
            url,
            headers=headers,
            json={
                "run": RUN,
                "status": status,
                "points": [{"step": step, "metrics": _metrics_for(step)} for step in steps],
            },
            timeout=15,
        )
        resp.raise_for_status()

    metrics_path = f"{PROJECT}/metrics.parquet"

    log([1, 2], "finished")
    _wait_for_flush(hf_api, repo_id, metrics_path)

    log([3, 4], "finished")

    deadline = time.monotonic() + FLUSH_TIMEOUT_SECONDS
    rows = 0
    while time.monotonic() < deadline:
        local = hf_hub_download(
            repo_id=repo_id,
            repo_type="dataset",
            filename=metrics_path,
            token=hf_token,
            force_download=True,
        )
        rows = pyarrow_parquet.read_table(local).num_rows
        if rows == 4:
            break
        time.sleep(FLUSH_POLL_INTERVAL_SECONDS)
    assert rows == 4, f"second flush left {rows} rows, want the union of both batches"

    series = _series(hf_endpoint, hf_token, namespace, unique_name)
    assert len(series[(RUN, "loss")]) == 4
