"""Verifies pushed files land at their content-addressed GCS keys, and that
no trace of the old `exports/` mirror remains.

The object-storage layout keys every byte range by the content it holds, not
by the repository it belongs to: a non-LFS git blob lands at
`blobs/{sha[0:2]}/{sha[2:4]}/{sha}` (sha = the git blob sha1,
`sha1("blob {len}\\0" + content)`), and an LFS object lands at
`lfs/{oid[0:2]}/{oid[2:4]}/{oid}` (oid = the content's sha256 hex). Both are
immutable and deduplicated across every repository -- nothing in the bucket
is named after a namespace, repository or path. When the server runs with
`GCS_PREFIX` set, every key below is additionally nested under that prefix
(the storage layer prepends it to all reads and writes, and to the `gs://`
URIs it hands back). `GET
/api/v1/repos/{kind}/{ns}/{name}/gcs/{rev}` (`apitypes.RepoGCSResponse`) is
what reconstructs that human-readable mapping, on the destination side of a
ready-made `gcloud storage cp` script and (when the revision has any
`.parquet` files) a DuckDB `read_parquet()` snippet.

This test talks to fake-gcs-server's JSON API directly (not through the
thinkingface API server) to confirm the objects really land at those keys
after a commit, and separately drives the `/gcs/{rev}` endpoint to check the
listing and the generated snippets it hands back.

Requires a running server + fake-gcs-server; see e2e/README.md.
"""

from __future__ import annotations

import hashlib
import os
import time
import urllib.parse

import requests
from huggingface_hub import HfApi

GCS_EMULATOR_URL = os.environ.get("GCS_EMULATOR_URL", "http://localhost:4443").rstrip("/")
GCS_BUCKET = os.environ.get("GCS_BUCKET", "thinkingface")
# The server prepends this to every object key it writes (and to the gs://
# URIs it returns), so the suite must look under it too. Empty -- the compose
# default -- means the bare `blobs/...` / `lfs/...` layout. TF_SSH_ENABLED /
# TF_WAL_MODE need no equivalent here: SSH reachability is negotiated through
# the advertised ssh_clone_url (conftest.py) and the WAL mode is purely
# server-internal.
GCS_PREFIX = os.environ.get("GCS_PREFIX", "").strip("/")
# The sync worker normally finishes within a second of the commit; 30s is
# slack for a loaded CI runner.
SYNC_TIMEOUT_SECONDS = 30
SYNC_POLL_INTERVAL_SECONDS = 1


def _git_blob_sha1(content: bytes) -> str:
    """The git blob hash: sha1("blob {len}\\0" + content)."""
    header = f"blob {len(content)}\0".encode()
    return hashlib.sha1(header + content).hexdigest()


def _lfs_oid(content: bytes) -> str:
    return hashlib.sha256(content).hexdigest()


def _blob_key(sha: str) -> str:
    return f"blobs/{sha[0:2]}/{sha[2:4]}/{sha}"


def _lfs_key(oid: str) -> str:
    return f"lfs/{oid[0:2]}/{oid[2:4]}/{oid}"


def _full_key(key: str) -> str:
    """Nest `key` under GCS_PREFIX, mirroring the server's storage layer."""
    return f"{GCS_PREFIX}/{key}" if GCS_PREFIX else key


def _list_objects(prefix: str) -> list[str]:
    """List object names under `prefix`, using fake-gcs-server's JSON API.

    https://cloud.google.com/storage/docs/json_api/v1/objects/list
    """
    resp = requests.get(
        f"{GCS_EMULATOR_URL}/storage/v1/b/{GCS_BUCKET}/o",
        params={"prefix": prefix},
        timeout=10,
    )
    resp.raise_for_status()
    return [item["name"] for item in resp.json().get("items", [])]


def _get_object_bytes(name: str) -> bytes:
    resp = requests.get(
        f"{GCS_EMULATOR_URL}/storage/v1/b/{GCS_BUCKET}/o/{urllib.parse.quote(name, safe='')}",
        params={"alt": "media"},
        timeout=10,
    )
    resp.raise_for_status()
    return resp.content


def _wait_for_content(name: str, expected: bytes, timeout: float = SYNC_TIMEOUT_SECONDS) -> bytes:
    """Poll until `name` holds `expected`, or the deadline passes."""
    deadline = time.monotonic() + timeout
    last = b""
    while time.monotonic() < deadline:
        try:
            last = _get_object_bytes(name)
        except requests.HTTPError:
            last = b""
        if last == expected:
            return last
        time.sleep(SYNC_POLL_INTERVAL_SECONDS)
    return last


def _wait_for_gcs_listing(
    endpoint: str, token: str, namespace: str, name: str, expected_paths: set[str]
) -> dict:
    """Poll GET /api/v1/repos/dataset/{ns}/{name}/gcs/main until repo_files
    has indexed every expected path, and return the parsed response body."""
    url = f"{endpoint}/api/v1/repos/dataset/{namespace}/{name}/gcs/main"
    headers = {"Authorization": f"Bearer {token}"}
    deadline = time.monotonic() + SYNC_TIMEOUT_SECONDS
    body: dict = {}
    while time.monotonic() < deadline:
        resp = requests.get(url, headers=headers, timeout=10)
        resp.raise_for_status()
        body = resp.json()
        paths = {f["path"] for f in body["files"]}
        if expected_paths <= paths:
            return body
        time.sleep(SYNC_POLL_INTERVAL_SECONDS)
    return body


def test_push_lands_at_content_addressed_keys_and_gcs_endpoint(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, namespace: str, unique_name: str
) -> None:
    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="dataset", private=False)
    try:
        readme_content = b"gcs layout smoke test\n"
        hf_api.upload_file(
            path_or_fileobj=readme_content,
            path_in_repo="README.md",
            repo_id=repo_id,
            repo_type="dataset",
            commit_message="Add README",
        )

        # *.parquet is LFS-tracked by default (design doc §3), same as
        # test_hf_compat.py.
        parquet_content = b"PAR1" + b"\x00" * 512 + b"thinkingface-e2e-gcs-layout"
        hf_api.upload_file(
            path_or_fileobj=parquet_content,
            path_in_repo="data/train.parquet",
            repo_id=repo_id,
            repo_type="dataset",
            commit_message="Add parquet data",
        )

        blob_key = _full_key(_blob_key(_git_blob_sha1(readme_content)))
        lfs_key = _full_key(_lfs_key(_lfs_oid(parquet_content)))

        # --- 1. content-addressed objects land at their promised keys ------
        blob_bytes = _wait_for_content(blob_key, readme_content)
        assert blob_bytes == readme_content, (
            f"{blob_key} did not reach the pushed README content within "
            f"{SYNC_TIMEOUT_SECONDS}s (has the sync worker published blobs/ "
            f"for this ref?); last read {blob_bytes!r}"
        )

        lfs_bytes = _wait_for_content(lfs_key, parquet_content)
        assert lfs_bytes == parquet_content, (
            f"{lfs_key} did not reach the uploaded LFS content within "
            f"{SYNC_TIMEOUT_SECONDS}s; last read {lfs_bytes!r}"
        )

        # --- 2. exports/ is gone: nothing lands there for this repo --------
        # Scoped to this repository's old prefix: a long-lived dev bucket may
        # still hold objects written by the retired mirror for other repos.
        exports_prefix = _full_key(f"exports/datasets/{namespace}/{unique_name}/")
        assert _list_objects(exports_prefix) == [], (
            f"found objects under {exports_prefix} -- the human-readable "
            "mirror was supposed to be fully removed"
        )

        # --- 3. GET /gcs/main reflects both files ---------------------------
        body = _wait_for_gcs_listing(
            hf_endpoint, hf_token, namespace, unique_name, {"README.md", "data/train.parquet"}
        )
        assert body["ref"] == "main"
        files_by_path = {f["path"]: f for f in body["files"]}
        assert {"README.md", "data/train.parquet"} <= set(files_by_path), body["files"]
        assert [f["path"] for f in body["files"]] == sorted(f["path"] for f in body["files"]), (
            "files must be path-sorted"
        )

        readme_file = files_by_path["README.md"]
        assert readme_file["lfs"] is False
        assert readme_file["uri"].startswith("gs://")
        assert readme_file["uri"].endswith(f"/{blob_key}")

        parquet_file = files_by_path["data/train.parquet"]
        assert parquet_file["lfs"] is True
        assert parquet_file["uri"].startswith("gs://")
        assert parquet_file["uri"].endswith(f"/{lfs_key}")

        # --- 4. gcloud_script matches the confirmed shell-script format -----
        script = body["gcloud_script"]
        assert script.startswith("#!/bin/sh\n")
        assert f'DEST="${{DEST:-./{unique_name}}}"' in script, script
        assert f"cp_one '{readme_file['uri']}' \"$DEST\"/'README.md'" in script, script
        assert f"cp_one '{parquet_file['uri']}' \"$DEST\"/'data/train.parquet'" in script, script

        # --- 5. duckdb_snippet lists every .parquet URI ---------------------
        snippet = body["duckdb_snippet"]
        assert "read_parquet([" in snippet
        assert parquet_file["uri"] in snippet
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="dataset")
