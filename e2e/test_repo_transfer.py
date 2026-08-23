"""End-to-end tests for repository ownership transfer
(docs/dev/repo-transfer-design.md, "POST /api/repos/move" and "Transfer (for the Web UI)"
in docs/dev/api-contract.md).

These exercise the HF-compatible `POST /api/repos/move`
(`huggingface_hub.HfApi.move_repo`) and the web UI's own
transfer/accept/reject/cancel endpoints, plus the redirect behaviour a moved
or renamed repository leaves behind at its old name across every protocol
surface: HF API (308), git smart HTTP (301), the UI JSON API (404 +
`repo_moved`), and the object-storage layer, where a transfer moves zero
bytes since objects are content-addressed rather than keyed by
namespace/name (see the bottom of this file).

Requires a running server; see e2e/README.md. `hf_api` / `hf_token` /
`namespace` come from conftest.py and act as the seeded admin user, who (being
a site admin) can complete any transfer immediately -- including ones on
repositories it does not own -- which is what makes it possible to drive the
"transfer to another, non-admin user" scenarios from a single bootstrapped
session plus one freshly signed-up second user per test.
"""

from __future__ import annotations

import hashlib
import os
import subprocess
import time
import urllib.parse
import uuid
from typing import NamedTuple

import pytest
import requests
from huggingface_hub import HfApi
from huggingface_hub.utils import HfHubHTTPError

GCS_EMULATOR_URL = os.environ.get("GCS_EMULATOR_URL", "http://localhost:4443").rstrip("/")
GCS_BUCKET = os.environ.get("GCS_BUCKET", "thinkingface")
# The sync worker normally finishes within a second of a push; 30s is slack
# for a loaded CI runner. Only used to wait for the *pre-move* upload to
# land -- the move itself triggers nothing async in object storage any more,
# see the gcs section at the bottom of this file.
GCS_SYNC_TIMEOUT_SECONDS = 30
GCS_SYNC_POLL_INTERVAL_SECONDS = 1


class OtherUser(NamedTuple):
    """A second, non-admin account with its own write-scoped token, used to
    exercise the "transfer to a namespace I don't control" approval path."""

    username: str
    token: str
    headers: dict[str, str]


def _signup(endpoint: str, username: str, password: str) -> requests.Session:
    session = requests.Session()
    resp = session.post(
        f"{endpoint}/api/v1/auth/signup",
        json={"username": username, "email": f"{username}@example.com", "password": password},
        timeout=10,
    )
    resp.raise_for_status()
    return session


def _mint_write_token(session: requests.Session, endpoint: str, name: str) -> tuple[int, str]:
    resp = session.post(
        f"{endpoint}/api/v1/tokens", json={"name": name, "scope": "write"}, timeout=10
    )
    resp.raise_for_status()
    body = resp.json()
    return body["id"], body["token"]


@pytest.fixture()
def other_user(hf_endpoint: str):
    """A freshly signed-up second user with a write-scoped token, revoked (and
    its bootstrap session closed) once the test finishes."""
    username = f"e2e-u-{uuid.uuid4().hex[:10]}"
    session = _signup(hf_endpoint, username, "password123")
    token_id, token = _mint_write_token(session, hf_endpoint, "e2e-transfer")
    try:
        yield OtherUser(
            username=username, token=token, headers={"Authorization": f"Bearer {token}"}
        )
    finally:
        try:
            session.delete(f"{hf_endpoint}/api/v1/tokens/{token_id}", timeout=10)
        except requests.RequestException:
            pass
        session.close()


def _move(
    endpoint: str, headers: dict[str, str], from_repo: str, to_repo: str, kind: str = "model"
) -> requests.Response:
    """POST /api/repos/move via plain `requests`, for tests that need to read
    the raw status code / body (200 vs. 202-pending) rather than relying on
    `HfApi.move_repo`, which treats every 2xx as success."""
    return requests.post(
        f"{endpoint}/api/repos/move",
        headers=headers,
        json={"fromRepo": from_repo, "toRepo": to_repo, "type": kind},
        timeout=10,
    )


def _delete_repo_ignore(hf_api: HfApi, repo_id: str, repo_type: str) -> None:
    """Best-effort cleanup: the repo may already be gone (a test that itself
    calls delete_repo), or may live under a different name than the caller
    expects if an assertion failed mid-test."""
    try:
        hf_api.delete_repo(repo_id=repo_id, repo_type=repo_type)
    except HfHubHTTPError:
        pass


def _list_objects(prefix: str) -> list[str]:
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


def _object_bytes_or_none(name: str) -> bytes | None:
    """Like _get_object_bytes, but None (rather than raising) when the object
    does not exist yet -- for use as a _wait_until predicate."""
    try:
        return _get_object_bytes(name)
    except requests.HTTPError:
        return None


def _lfs_key(content: bytes) -> str:
    oid = hashlib.sha256(content).hexdigest()
    return f"lfs/{oid[0:2]}/{oid[2:4]}/{oid}"


def _wait_until(predicate, timeout: float = GCS_SYNC_TIMEOUT_SECONDS) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return True
        time.sleep(GCS_SYNC_POLL_INTERVAL_SECONDS)
    return predicate()


# --------------------------------------------------------------- immediate


def test_move_repo_rename_and_old_name_redirect(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, namespace: str, unique_name: str
) -> None:
    """A same-namespace `move_repo` (a rename) completes immediately (200),
    and the old id keeps working -- reads and writes alike -- via redirect."""
    from huggingface_hub import hf_hub_download

    old_name = unique_name
    new_name = f"{unique_name}-renamed"
    from_id = f"{namespace}/{old_name}"
    to_id = f"{namespace}/{new_name}"

    hf_api.create_repo(repo_id=from_id, repo_type="model", private=False)
    current_id = from_id
    try:
        readme_text = "# transfer e2e\n\nsmall text file, non-LFS.\n"
        hf_api.upload_file(
            path_or_fileobj=readme_text.encode("utf-8"),
            path_in_repo="README.md",
            repo_id=from_id,
            repo_type="model",
            commit_message="Add README",
        )
        # *.safetensors is LFS-tracked by default, same as test_hf_compat.py.
        checkpoint = b"\x00" * 4096 + b"thinkingface-e2e-transfer-weights"
        hf_api.upload_file(
            path_or_fileobj=checkpoint,
            path_in_repo="model.safetensors",
            repo_id=from_id,
            repo_type="model",
            commit_message="Add checkpoint",
        )

        hf_api.move_repo(from_id=from_id, to_id=to_id, repo_type="model")
        current_id = to_id

        info = hf_api.repo_info(repo_id=to_id, repo_type="model")
        assert info.id == to_id

        files = hf_api.list_repo_files(repo_id=to_id, repo_type="model")
        assert "README.md" in files
        assert "model.safetensors" in files

        downloaded_readme = hf_hub_download(repo_id=to_id, repo_type="model", filename="README.md")
        with open(downloaded_readme, encoding="utf-8") as f:
            assert f.read() == readme_text

        downloaded_ckpt = hf_hub_download(
            repo_id=to_id, repo_type="model", filename="model.safetensors"
        )
        with open(downloaded_ckpt, "rb") as f:
            assert f.read() == checkpoint

        # --- the old id still resolves, via 308 redirects --------------------
        old_info = hf_api.repo_info(repo_id=from_id, repo_type="model")
        assert old_info.id == to_id

        old_readme = hf_hub_download(repo_id=from_id, repo_type="model", filename="README.md")
        with open(old_readme, encoding="utf-8") as f:
            assert f.read() == readme_text

        old_ckpt = hf_hub_download(repo_id=from_id, repo_type="model", filename="model.safetensors")
        with open(old_ckpt, "rb") as f:
            assert f.read() == checkpoint

        # write via the old id also redirects (huggingface_hub/requests keep
        # POST across a 307/308), and lands on the new repo.
        via_old_name = b"written through the old repo id\n"
        hf_api.upload_file(
            path_or_fileobj=via_old_name,
            path_in_repo="via-old-name.txt",
            repo_id=from_id,
            repo_type="model",
            commit_message="Add file via old repo id",
        )
        files_after = hf_api.list_repo_files(repo_id=to_id, repo_type="model")
        assert "via-old-name.txt" in files_after
    finally:
        _delete_repo_ignore(hf_api, current_id, "model")


def test_move_repo_name_conflict_returns_409(
    hf_api: HfApi, namespace: str, unique_name: str
) -> None:
    repo_a = f"{namespace}/{unique_name}-a"
    repo_b = f"{namespace}/{unique_name}-b"
    hf_api.create_repo(repo_id=repo_a, repo_type="model", private=False)
    hf_api.create_repo(repo_id=repo_b, repo_type="model", private=False)
    try:
        with pytest.raises(HfHubHTTPError) as excinfo:
            hf_api.move_repo(from_id=repo_a, to_id=repo_b, repo_type="model")
        assert excinfo.value.response.status_code == 409
    finally:
        _delete_repo_ignore(hf_api, repo_a, "model")
        _delete_repo_ignore(hf_api, repo_b, "model")


# ------------------------------------------------------- approval workflow


def test_transfer_to_other_user_requires_approval_then_accept(
    hf_api: HfApi,
    hf_endpoint: str,
    hf_token: str,
    namespace: str,
    unique_name: str,
    other_user: OtherUser,
) -> None:
    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    current_ns = namespace
    try:
        admin_headers = {"Authorization": f"Bearer {hf_token}"}

        # The site admin can complete a transfer to *any* namespace
        # immediately, including one it doesn't itself control.
        to_other = _move(
            hf_endpoint, admin_headers, repo_id, f"{other_user.username}/{unique_name}"
        )
        assert to_other.status_code == 200, to_other.text
        current_ns = other_user.username

        moved_info = hf_api.repo_info(
            repo_id=f"{other_user.username}/{unique_name}", repo_type="model"
        )
        assert moved_info.id == f"{other_user.username}/{unique_name}"

        # The other user is not a site admin and has no write access to
        # admin's namespace, so moving it back must become a pending request.
        back_to_admin = _move(
            hf_endpoint, other_user.headers, f"{other_user.username}/{unique_name}", repo_id
        )
        assert back_to_admin.status_code == 202, back_to_admin.text
        body = back_to_admin.json()
        assert body["pending"] is True
        transfer_id = body["transfer_id"]

        # admin sees it in their incoming list before deciding.
        incoming_resp = requests.get(
            f"{hf_endpoint}/api/v1/me/transfers", headers=admin_headers, timeout=10
        )
        incoming_resp.raise_for_status()
        incoming = incoming_resp.json()["incoming"]
        assert any(t["id"] == transfer_id and t["status"] == "pending" for t in incoming)

        # the other user sees the same request in their outgoing list.
        outgoing_resp = requests.get(
            f"{hf_endpoint}/api/v1/me/transfers", headers=other_user.headers, timeout=10
        )
        outgoing_resp.raise_for_status()
        outgoing = outgoing_resp.json()["outgoing"]
        assert any(t["id"] == transfer_id and t["status"] == "pending" for t in outgoing)

        accept_resp = requests.post(
            f"{hf_endpoint}/api/v1/transfers/{transfer_id}/accept",
            headers=admin_headers,
            timeout=10,
        )
        assert accept_resp.status_code == 200, accept_resp.text
        accepted = accept_resp.json()
        assert accepted["transfer"]["status"] == "accepted"
        assert accepted["repo"]["full_name"] == repo_id
        current_ns = namespace

        final_info = hf_api.repo_info(repo_id=repo_id, repo_type="model")
        assert final_info.id == repo_id
    finally:
        _delete_repo_ignore(hf_api, f"{current_ns}/{unique_name}", "model")


def test_transfer_pending_can_be_rejected(
    hf_api: HfApi,
    hf_endpoint: str,
    hf_token: str,
    namespace: str,
    unique_name: str,
    other_user: OtherUser,
) -> None:
    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    current_ns = namespace
    try:
        admin_headers = {"Authorization": f"Bearer {hf_token}"}
        to_other = _move(
            hf_endpoint, admin_headers, repo_id, f"{other_user.username}/{unique_name}"
        )
        assert to_other.status_code == 200, to_other.text
        current_ns = other_user.username

        pending = _move(
            hf_endpoint, other_user.headers, f"{other_user.username}/{unique_name}", repo_id
        )
        assert pending.status_code == 202, pending.text
        transfer_id = pending.json()["transfer_id"]

        reject_resp = requests.post(
            f"{hf_endpoint}/api/v1/transfers/{transfer_id}/reject",
            headers=admin_headers,
            timeout=10,
        )
        assert reject_resp.status_code == 200, reject_resp.text
        assert reject_resp.json()["transfer"]["status"] == "rejected"

        # the repository never moved: it is still with the other user.
        info = hf_api.repo_info(repo_id=f"{other_user.username}/{unique_name}", repo_type="model")
        assert info.id == f"{other_user.username}/{unique_name}"
    finally:
        _delete_repo_ignore(hf_api, f"{current_ns}/{unique_name}", "model")


def test_transfer_pending_can_be_cancelled_by_originator(
    hf_api: HfApi,
    hf_endpoint: str,
    hf_token: str,
    namespace: str,
    unique_name: str,
    other_user: OtherUser,
) -> None:
    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    current_ns = namespace
    try:
        admin_headers = {"Authorization": f"Bearer {hf_token}"}
        to_other = _move(
            hf_endpoint, admin_headers, repo_id, f"{other_user.username}/{unique_name}"
        )
        assert to_other.status_code == 200, to_other.text
        current_ns = other_user.username

        pending = _move(
            hf_endpoint, other_user.headers, f"{other_user.username}/{unique_name}", repo_id
        )
        assert pending.status_code == 202, pending.text

        transfer_url = (
            f"{hf_endpoint}/api/v1/repos/model/{other_user.username}/{unique_name}/transfer"
        )
        get_pending = requests.get(transfer_url, headers=other_user.headers, timeout=10)
        assert get_pending.status_code == 200, get_pending.text
        assert get_pending.json()["transfer"]["status"] == "pending"

        cancel_resp = requests.delete(transfer_url, headers=other_user.headers, timeout=10)
        assert cancel_resp.status_code == 204, cancel_resp.text

        after_cancel = requests.get(transfer_url, headers=other_user.headers, timeout=10)
        assert after_cancel.status_code == 404, after_cancel.text

        # the repository never moved.
        info = hf_api.repo_info(repo_id=f"{other_user.username}/{unique_name}", repo_type="model")
        assert info.id == f"{other_user.username}/{unique_name}"
    finally:
        _delete_repo_ignore(hf_api, f"{current_ns}/{unique_name}", "model")


# ------------------------------------------------------------------- UI API


def test_old_name_ui_api_returns_repo_moved(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, namespace: str, unique_name: str
) -> None:
    old_name = unique_name
    new_name = f"{unique_name}-moved"
    repo_id = f"{namespace}/{old_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    current_name = old_name
    try:
        hf_api.move_repo(from_id=repo_id, to_id=f"{namespace}/{new_name}", repo_type="model")
        current_name = new_name

        resp = requests.get(
            f"{hf_endpoint}/api/v1/repos/model/{namespace}/{old_name}",
            headers={"Authorization": f"Bearer {hf_token}"},
            timeout=10,
        )
        assert resp.status_code == 404, resp.text
        body = resp.json()
        assert body["error"]["type"] == "repo_moved"
        assert body["error"]["moved_to"] == {"namespace": namespace, "name": new_name}
    finally:
        _delete_repo_ignore(hf_api, f"{namespace}/{current_name}", "model")


# ----------------------------------------------------------------------- git


def test_git_clone_and_pull_follow_redirect_after_move(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, namespace: str, unique_name: str, tmp_path
) -> None:
    old_name = unique_name
    new_name = f"{unique_name}-git-moved"
    repo_id = f"{namespace}/{old_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    current_name = old_name
    try:
        hf_api.upload_file(
            path_or_fileobj=b"git redirect e2e\n",
            path_in_repo="README.md",
            repo_id=repo_id,
            repo_type="model",
            commit_message="Add README",
        )

        parsed = urllib.parse.urlsplit(hf_endpoint)
        netloc = f"{namespace}:{hf_token}@{parsed.netloc}"
        old_url = urllib.parse.urlunsplit(
            (parsed.scheme, netloc, f"/{namespace}/{old_name}.git", "", "")
        )

        clone_dir = tmp_path / "clone"
        env = {**os.environ, "GIT_TERMINAL_PROMPT": "0"}
        subprocess.run(
            ["git", "clone", old_url, str(clone_dir)],
            check=True,
            capture_output=True,
            env=env,
            timeout=30,
        )
        readme_path = clone_dir / "README.md"
        assert readme_path.read_text() == "git redirect e2e\n"

        hf_api.move_repo(from_id=repo_id, to_id=f"{namespace}/{new_name}", repo_type="model")
        current_name = new_name

        # the existing clone still points at the old URL...
        remote = subprocess.run(
            ["git", "-C", str(clone_dir), "remote", "get-url", "origin"],
            check=True,
            capture_output=True,
            text=True,
            env=env,
            timeout=10,
        ).stdout.strip()
        assert remote == old_url

        # ...but info/refs answers 301 (git's default
        # http.followRedirects=initial follows it), so a pull still works.
        hf_api.upload_file(
            path_or_fileobj=b"git redirect e2e, updated\n",
            path_in_repo="README.md",
            repo_id=f"{namespace}/{new_name}",
            repo_type="model",
            commit_message="Update via new name",
        )
        subprocess.run(
            ["git", "-C", str(clone_dir), "pull"],
            check=True,
            capture_output=True,
            env=env,
            timeout=30,
        )
        assert readme_path.read_text() == "git redirect e2e, updated\n"
    finally:
        _delete_repo_ignore(hf_api, f"{namespace}/{current_name}", "model")


# --------------------------------------------------------------------- gcs


def test_gcs_objects_stay_put_after_transfer(
    hf_api: HfApi,
    hf_endpoint: str,
    hf_token: str,
    namespace: str,
    unique_name: str,
    other_user: OtherUser,
) -> None:
    """A transfer moves zero bytes in the bucket: object keys are derived
    from content (lfs/{oid}), not from (namespace, name), so the LFS object
    uploaded before the move is still readable at the exact same key
    afterwards. There is also no `exports/` mirror left to relocate any more
    -- it never existed under either name -- and the new name's `/gcs/main`
    listing resolves to that same, unmoved URI."""
    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="dataset", private=False)
    current_ns = namespace
    try:
        content = b"gcs relocation e2e\n"
        # *.parquet is LFS-tracked by default (design doc §3), same as
        # test_hf_compat.py -- exercises the lfs/ content-addressed path
        # rather than blobs/.
        hf_api.upload_file(
            path_or_fileobj=content,
            path_in_repo="data/train.parquet",
            repo_id=repo_id,
            repo_type="dataset",
            commit_message="Trigger initial LFS upload",
        )
        lfs_key = _lfs_key(content)

        # Make sure the pre-move object actually landed before moving, so the
        # post-move "still there, unmoved" assertion is meaningful.
        assert _wait_until(
            lambda: _object_bytes_or_none(lfs_key) == content
        ), f"{lfs_key} never reached the uploaded content before the move"

        admin_headers = {"Authorization": f"Bearer {hf_token}"}
        move_resp = _move(
            hf_endpoint,
            admin_headers,
            repo_id,
            f"{other_user.username}/{unique_name}",
            kind="dataset",
        )
        assert move_resp.status_code == 200, move_resp.text
        current_ns = other_user.username

        # (a) the LFS object is exactly where it was -- a transfer is a pure
        # DB rename, no object-storage copy is queued or needed.
        assert _get_object_bytes(lfs_key) == content

        # (b) exports/ never existed under either name, old or new.
        assert _list_objects(f"exports/datasets/{namespace}/{unique_name}/") == []
        assert _list_objects(f"exports/datasets/{other_user.username}/{unique_name}/") == []

        # (c) the new name's /gcs/main listing points at that same URI.
        gcs_resp = requests.get(
            f"{hf_endpoint}/api/v1/repos/dataset/{other_user.username}/{unique_name}/gcs/main",
            headers=admin_headers,
            timeout=10,
        )
        gcs_resp.raise_for_status()
        files_by_path = {f["path"]: f for f in gcs_resp.json()["files"]}
        assert files_by_path["data/train.parquet"]["uri"].endswith(f"/{lfs_key}")
    finally:
        _delete_repo_ignore(hf_api, f"{current_ns}/{unique_name}", "dataset")
