"""End-to-end compatibility tests: does `huggingface_hub` work unmodified
against a thinkingface server?

These exercise the HF-compatible REST surface described in
docs/dev/api-contract.md (whoami-v2, repos/create, preupload, commit, resolve,
tree) purely through the public `huggingface_hub` / `datasets` APIs -- no
thinkingface-specific client code -- since that "no client changes needed"
property is the whole point of the design (docs/dev/thinkingface-design.md §2).

Requires a running server; see e2e/README.md. These pass against the current
backend -- treat a failure here as a compatibility regression, not as an
unimplemented endpoint.
"""

from __future__ import annotations

import io

import pyarrow as pa
import pyarrow.parquet as pq
import pytest
from huggingface_hub import HfApi
from huggingface_hub.utils import HfHubHTTPError


def test_whoami(hf_api: HfApi, namespace: str) -> None:
    info = hf_api.whoami()
    assert info["name"] == namespace
    assert info["type"] == "user"


def test_dataset_upload_download_and_lfs_roundtrip(
    hf_api: HfApi, namespace: str, unique_name: str, tmp_path
) -> None:
    from huggingface_hub import hf_hub_download

    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="dataset", private=False)
    try:
        # --- plain (non-LFS) file: README.md -----------------------------
        readme_text = "# e2e dataset\n\nCreated by the thinkingface e2e suite.\n"
        hf_api.upload_file(
            path_or_fileobj=readme_text.encode("utf-8"),
            path_in_repo="README.md",
            repo_id=repo_id,
            repo_type="dataset",
            commit_message="Add README",
        )

        downloaded_readme = hf_hub_download(
            repo_id=repo_id, repo_type="dataset", filename="README.md"
        )
        with open(downloaded_readme, encoding="utf-8") as f:
            assert f.read() == readme_text

        # --- parquet file: exercises the LFS batch/upload path -----------
        # *.parquet is LFS-tracked by default (design doc §3), regardless
        # of size, so even this small table goes through the LFS path.
        table = pa.table(
            {"id": pa.array(range(50)), "text": pa.array([f"row-{i}" for i in range(50)])}
        )
        buf = io.BytesIO()
        pq.write_table(table, buf)
        buf.seek(0)

        hf_api.upload_file(
            path_or_fileobj=buf,
            path_in_repo="data/train.parquet",
            repo_id=repo_id,
            repo_type="dataset",
            commit_message="Add parquet data",
        )

        files = hf_api.list_repo_files(repo_id=repo_id, repo_type="dataset")
        assert "data/train.parquet" in files
        assert "README.md" in files

        downloaded_parquet = hf_hub_download(
            repo_id=repo_id, repo_type="dataset", filename="data/train.parquet"
        )
        read_back = pq.read_table(downloaded_parquet)
        assert read_back.num_rows == 50

        # --- list_repo_tree returns both files and directories -----------
        tree = list(hf_api.list_repo_tree(repo_id=repo_id, repo_type="dataset", recursive=True))
        paths = {entry.path for entry in tree}
        assert "README.md" in paths
        assert "data/train.parquet" in paths
        assert "data" in paths  # the directory entry itself

        # --- datasets.load_dataset reads the downloaded parquet ----------
        from datasets import load_dataset

        ds = load_dataset("parquet", data_files=downloaded_parquet)
        assert len(ds["train"]) == 50
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="dataset")

    with pytest.raises(HfHubHTTPError):
        hf_api.repo_info(repo_id=repo_id, repo_type="dataset")


def test_model_upload_download_and_delete(hf_api: HfApi, namespace: str, unique_name: str) -> None:
    from huggingface_hub import hf_hub_download

    repo_id = f"{namespace}/{unique_name}-model"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    try:
        # Stand-in for a real safetensors checkpoint: *.safetensors is
        # LFS-tracked by default, same as *.parquet above.
        payload = b"\x00" * 4096 + b"thinkingface-e2e-weights"
        hf_api.upload_file(
            path_or_fileobj=payload,
            path_in_repo="model.safetensors",
            repo_id=repo_id,
            repo_type="model",
            commit_message="Add checkpoint",
        )

        downloaded = hf_hub_download(
            repo_id=repo_id, repo_type="model", filename="model.safetensors"
        )
        with open(downloaded, "rb") as f:
            assert f.read() == payload
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model")

    with pytest.raises(HfHubHTTPError):
        hf_api.repo_info(repo_id=repo_id, repo_type="model")
