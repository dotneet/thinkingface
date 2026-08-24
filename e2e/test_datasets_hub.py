"""`datasets.load_dataset("{namespace}/{name}")` over the Hub path.

This is the one test file that covers the second of the three pillars in
docs/dev/thinkingface-design.md §1: `datasets.load_dataset()` working against
this server with zero client-side changes.

The distinction from test_hf_compat.py's parquet roundtrip matters. That test
calls `load_dataset("parquet", data_files=<local path>)`, i.e. it hands
`datasets` a file that `hf_hub_download` already fetched -- the loader never
touches the server. Here the *repo id* is the argument, so `datasets` has to
walk the whole Hub path itself:

    HfApi.dataset_info  ->  the repo exists, at which revision
    HfFileSystem        ->  glob the tree to infer configs and splits
    /resolve/{rev}/...  ->  stream the parquet bytes (LFS-backed)

Split auto-detection is part of the contract (design doc §14, Phase 1:
"`datasets.load_dataset("admin/imdb-splits")` works, including automatic
train/test split detection"), which is why the fixture below uploads the
canonical `data/{split}-00000-of-00001.parquet` layout rather than a single
file: the split names come from the tree, not from an argument.

Requires a running server; see e2e/README.md.
"""

from __future__ import annotations

import io

import pyarrow as pa
import pyarrow.parquet as pq
import pytest
from huggingface_hub import HfApi

# Rows per split, deliberately different so a test cannot pass by reading the
# wrong split.
TRAIN_ROWS = 24
TEST_ROWS = 8


def _parquet_bytes(*, offset: int, rows: int, label: str) -> io.BytesIO:
    table = pa.table(
        {
            "id": pa.array(range(offset, offset + rows), type=pa.int64()),
            "text": pa.array([f"{label}-{i}" for i in range(rows)]),
            "label": pa.array([i % 2 for i in range(rows)], type=pa.int64()),
        }
    )
    buf = io.BytesIO()
    pq.write_table(table, buf)
    buf.seek(0)
    return buf


def _upload_split_dataset(hf_api: HfApi, repo_id: str) -> None:
    """Upload the `data/{split}-00000-of-00001.parquet` layout HF uses.

    `datasets` derives the split names from these file names, so the shape
    here is load-bearing -- renaming the files to `data/a.parquet` /
    `data/b.parquet` would collapse everything into a single `train` split.
    """
    hf_api.upload_file(
        path_or_fileobj=_parquet_bytes(offset=0, rows=TRAIN_ROWS, label="train").read(),
        path_in_repo="data/train-00000-of-00001.parquet",
        repo_id=repo_id,
        repo_type="dataset",
        commit_message="Add train split",
    )
    hf_api.upload_file(
        path_or_fileobj=_parquet_bytes(offset=1000, rows=TEST_ROWS, label="test").read(),
        path_in_repo="data/test-00000-of-00001.parquet",
        repo_id=repo_id,
        repo_type="dataset",
        commit_message="Add test split",
    )


@pytest.fixture()
def split_dataset_repo(hf_api: HfApi, namespace: str, unique_name: str):
    """A dataset repo with train/test parquet splits, deleted afterwards."""
    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="dataset", private=False)
    try:
        _upload_split_dataset(hf_api, repo_id)
        yield repo_id
    finally:
        try:
            hf_api.delete_repo(repo_id=repo_id, repo_type="dataset")
        except Exception:  # noqa: BLE001 - cleanup must not mask a test failure
            pass


def test_load_dataset_by_repo_id_detects_splits(split_dataset_repo: str, tmp_path) -> None:
    """The headline case: `load_dataset("{ns}/{name}")` with no other hints."""
    from datasets import load_dataset

    ds = load_dataset(split_dataset_repo, cache_dir=str(tmp_path / "cache"))

    # Split auto-detection: both splits, and nothing invented on top.
    assert set(ds.keys()) == {"train", "test"}
    assert len(ds["train"]) == TRAIN_ROWS
    assert len(ds["test"]) == TEST_ROWS

    # The schema survived the parquet -> LFS -> resolve -> datasets trip.
    assert set(ds["train"].column_names) == {"id", "text", "label"}

    # And the actual bytes are the ones uploaded, not some other split's.
    assert ds["train"][0]["text"] == "train-0"
    assert ds["train"][0]["id"] == 0
    assert ds["test"][0]["text"] == "test-0"
    assert ds["test"][0]["id"] == 1000


def test_load_dataset_with_an_explicit_split(split_dataset_repo: str, tmp_path) -> None:
    """`split="train"` returns a bare Dataset, not a DatasetDict."""
    from datasets import Dataset, load_dataset

    ds = load_dataset(split_dataset_repo, split="train", cache_dir=str(tmp_path / "cache"))

    assert isinstance(ds, Dataset)
    assert len(ds) == TRAIN_ROWS
    assert ds[TRAIN_ROWS - 1]["text"] == f"train-{TRAIN_ROWS - 1}"


def test_load_dataset_streaming_reads_over_the_hub(split_dataset_repo: str) -> None:
    """Streaming never downloads the file: it range-reads it through
    HfFileSystem, so it exercises a different server path (resolve with a
    Range header / redirect to storage) than the cached download above."""
    from datasets import load_dataset

    ds = load_dataset(split_dataset_repo, split="train", streaming=True)

    rows = list(ds.take(3))
    assert [row["text"] for row in rows] == ["train-0", "train-1", "train-2"]


def test_load_dataset_at_a_revision(hf_api: HfApi, split_dataset_repo: str, tmp_path) -> None:
    """`revision=` is forwarded to dataset_info / resolve, so a tag pins the
    data even after the branch moves on."""
    from datasets import load_dataset

    hf_api.create_tag(repo_id=split_dataset_repo, repo_type="dataset", tag="v1")

    # Move main on: a third split that only exists after the tag.
    hf_api.upload_file(
        path_or_fileobj=_parquet_bytes(offset=2000, rows=4, label="validation").read(),
        path_in_repo="data/validation-00000-of-00001.parquet",
        repo_id=split_dataset_repo,
        repo_type="dataset",
        commit_message="Add validation split",
    )

    at_tag = load_dataset(split_dataset_repo, revision="v1", cache_dir=str(tmp_path / "cache-tag"))
    assert set(at_tag.keys()) == {"train", "test"}

    at_main = load_dataset(split_dataset_repo, cache_dir=str(tmp_path / "cache-main"))
    assert set(at_main.keys()) == {"train", "test", "validation"}
    assert len(at_main["validation"]) == 4


def test_load_dataset_on_a_missing_repo_raises(namespace: str, unique_name: str) -> None:
    """A 404 from dataset_info has to surface as an error, not as an empty
    dataset -- the failure mode that would make every other assertion here
    vacuous."""
    from datasets import load_dataset

    with pytest.raises(Exception):  # noqa: B017 - datasets wraps this differently per version
        load_dataset(f"{namespace}/{unique_name}-does-not-exist")
