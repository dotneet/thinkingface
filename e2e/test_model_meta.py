"""End-to-end tests for the checkpoint metadata endpoint:

    GET /api/v1/model-meta/{kind}/{ns}/{name}/{rev}/{path...}

This reads a safetensors or PyTorch checkpoint's header -- tensor list,
dtypes, parameter counts, embedded metadata -- without downloading the
weights. It sits alongside (not inside) the `huggingface_hub`-compatible
surface tested in `test_hf_compat.py`, so it's exercised with plain
`requests` calls, authenticated the same way `huggingface_hub` is (a Bearer
token from the `hf_token` fixture). Checkpoint bytes come from
`fixtures_checkpoints.py`, which builds them without requiring `torch` to be
installed (see that module's docstring for why).

Also covers the UI tree endpoint's checkpoint-awareness:

    GET /api/v1/repos/{kind}/{ns}/{name}/tree/{rev}
"""

from __future__ import annotations

import pytest
import requests
from huggingface_hub import HfApi

from fixtures_checkpoints import safetensors_file, torch_checkpoint


@pytest.fixture()
def model_repo(hf_api: HfApi, hf_endpoint: str, hf_token: str, namespace: str, unique_name: str):
    """A model repo pre-populated with checkpoints exercising both storage
    paths: `.safetensors`/`.bin` at the repo root go through LFS (they match
    the default `.gitattributes`), while `inline/small.safetensors` is
    uploaded after a `.gitattributes` that drops the `*.safetensors` LFS
    rule, so it falls back to the file-size threshold and lands as a plain
    git blob -- the other code path `handleModelMeta` supports.
    """
    repo_id = f"{namespace}/{unique_name}-model"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    try:
        hf_api.upload_file(
            path_or_fileobj=safetensors_file(),
            path_in_repo="model.safetensors",
            repo_id=repo_id,
            repo_type="model",
            commit_message="Add safetensors checkpoint",
        )
        hf_api.upload_file(
            path_or_fileobj=torch_checkpoint(),
            path_in_repo="pytorch_model.bin",
            repo_id=repo_id,
            repo_type="model",
            commit_message="Add pytorch checkpoint",
        )
        hf_api.upload_file(
            path_or_fileobj=b"# a model\n",
            path_in_repo="README.md",
            repo_id=repo_id,
            repo_type="model",
            commit_message="Add README",
        )
        # Drop the default *.safetensors LFS rule so the next upload is
        # stored as a plain git blob instead.
        hf_api.upload_file(
            path_or_fileobj=b"*.parquet filter=lfs diff=lfs merge=lfs -text\n",
            path_in_repo=".gitattributes",
            repo_id=repo_id,
            repo_type="model",
            commit_message="Untrack safetensors from LFS",
        )
        hf_api.upload_file(
            path_or_fileobj=safetensors_file(),
            path_in_repo="inline/small.safetensors",
            repo_id=repo_id,
            repo_type="model",
            commit_message="Add non-LFS safetensors checkpoint",
        )
        yield repo_id
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model")


def _meta(hf_endpoint: str, hf_token: str, repo_id: str, path: str) -> requests.Response:
    ns, name = repo_id.split("/")
    return requests.get(
        f"{hf_endpoint}/api/v1/model-meta/model/{ns}/{name}/main/{path}",
        headers={"Authorization": f"Bearer {hf_token}"},
        timeout=10,
    )


def test_safetensors_lfs(hf_endpoint: str, hf_token: str, model_repo: str) -> None:
    resp = _meta(hf_endpoint, hf_token, model_repo, "model.safetensors")
    assert resp.status_code == 200, resp.text
    body = resp.json()

    assert body["num_tensors"] == 3
    assert body["num_parameters"] == 4 * 8 + 4 + 3 * 4

    # Tensors come back in the order they sit in the file (their
    # data_offsets), not e.g. alphabetically.
    names = [t["name"] for t in body["tensors"]]
    assert names == ["encoder.weight", "encoder.bias", "head.weight"]

    dtypes = {t["name"]: t["dtype"] for t in body["tensors"]}
    assert dtypes["encoder.weight"] == "float32"
    assert dtypes["head.weight"] == "bfloat16"

    assert body["metadata"]["modelspec.title"] == "tiny-mlp"


def test_pytorch_bin_lfs(hf_endpoint: str, hf_token: str, model_repo: str) -> None:
    resp = _meta(hf_endpoint, hf_token, model_repo, "pytorch_model.bin")
    assert resp.status_code == 200, resp.text
    body = resp.json()

    assert body["num_tensors"] == 4
    tensors = {t["name"]: t for t in body["tensors"]}

    # state_dict keys are flattened into dot-joined names.
    assert set(tensors) == {
        "state_dict.encoder.weight",
        "state_dict.encoder.bias",
        "state_dict.head.weight",
        "state_dict.step_counter",
    }

    assert tensors["state_dict.encoder.weight"]["dtype"] == "float32"
    assert tensors["state_dict.head.weight"]["dtype"] == "float16"
    assert tensors["state_dict.step_counter"]["dtype"] == "int64"
    # A 0-dimensional tensor has an empty shape, not e.g. [1].
    assert tensors["state_dict.step_counter"]["shape"] == []

    assert body["metadata"]["epoch"] == "7"
    assert body["metadata"]["global_step"] == "1234"
    assert body["metadata"]["arch"] == "tiny-mlp"


def test_safetensors_plain_git_blob(hf_endpoint: str, hf_token: str, model_repo: str) -> None:
    """Same file format, but stored as an ordinary git blob (see the
    `model_repo` fixture) instead of an LFS object -- exercises the other
    read path in `checkpointSource`, and should report identical metadata.
    """
    resp = _meta(hf_endpoint, hf_token, model_repo, "inline/small.safetensors")
    assert resp.status_code == 200, resp.text
    body = resp.json()
    assert body["num_tensors"] == 3
    assert body["num_parameters"] == 4 * 8 + 4 + 3 * 4
    assert [t["name"] for t in body["tensors"]] == ["encoder.weight", "encoder.bias", "head.weight"]


def test_non_checkpoint_extension_rejected(
    hf_endpoint: str, hf_token: str, model_repo: str
) -> None:
    resp = _meta(hf_endpoint, hf_token, model_repo, "README.md")
    assert resp.status_code == 400


def test_tree_flags_checkpoints(hf_endpoint: str, hf_token: str, model_repo: str) -> None:
    ns, name = model_repo.split("/")
    resp = requests.get(
        f"{hf_endpoint}/api/v1/repos/model/{ns}/{name}/tree/main",
        headers={"Authorization": f"Bearer {hf_token}"},
        timeout=10,
    )
    assert resp.status_code == 200, resp.text
    entries = {e["name"]: e for e in resp.json()["entries"]}

    assert entries["model.safetensors"]["preview"] == "model"
    assert entries["model.safetensors"]["is_model"] is True
    assert entries["pytorch_model.bin"]["preview"] == "model"
    assert entries["pytorch_model.bin"]["is_model"] is True

    assert entries["README.md"]["is_model"] is False
