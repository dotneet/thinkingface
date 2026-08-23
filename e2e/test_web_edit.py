"""End-to-end tests for the web UI's in-browser file editor:

    PUT /api/v1/edit/{kind}/{ns}/{name}/{rev}/{path...}

Unlike `test_hf_compat.py` / `test_model_meta.py`, this is driven with a
plain `requests.Session` authenticated via the cookie-based
`/api/v1/auth/login` endpoint (Bearer tokens work too, per
`internal/api/auth.go`, but the web UI itself uses the session cookie, so
that's what this suite exercises). Repos are still created and populated
through `hf_api` (the `huggingface_hub` client) since that path is already
covered and it keeps setup terse.
"""

from __future__ import annotations

import os
from collections.abc import Iterator
from typing import NamedTuple

import pytest
import requests
from huggingface_hub import HfApi

from fixtures_checkpoints import safetensors_file

# Same defaults/env-var names as conftest.py's bootstrap, kept local to this
# module since conftest's fixtures are session-scoped around a token login,
# not a cookie session.
TF_ADMIN_USERNAME = os.environ.get("TF_ADMIN_USERNAME", "admin")
TF_ADMIN_PASSWORD = os.environ.get("TF_ADMIN_PASSWORD", "admin")

INITIAL_README = "# e2e web-edit\n\noriginal content\n"


class EditRepo(NamedTuple):
    """A model repo with a README.md and an LFS-tracked checkpoint, plus an
    authenticated (cookie) session belonging to the repo's own owner."""

    repo_id: str
    ns: str
    name: str
    endpoint: str
    session: requests.Session

    def url(self, suffix: str) -> str:
        return f"{self.endpoint}{suffix}"

    def edit_url(self, path: str, rev: str = "main") -> str:
        return self.url(f"/api/v1/edit/model/{self.ns}/{self.name}/{rev}/{path}")

    def root_oid(self, path: str) -> str:
        """The current blob oid for a root-level file, read via the UI tree
        endpoint (the same field `handleEditFile` compares `base_oid`
        against)."""
        resp = self.session.get(self.url(f"/api/v1/repos/model/{self.ns}/{self.name}/tree/main"))
        resp.raise_for_status()
        entries = {e["name"]: e for e in resp.json()["entries"]}
        return entries[path]["oid"]


def _login_session(endpoint: str) -> requests.Session:
    session = requests.Session()
    resp = session.post(
        f"{endpoint}/api/v1/auth/login",
        json={"username": TF_ADMIN_USERNAME, "password": TF_ADMIN_PASSWORD},
        timeout=10,
    )
    resp.raise_for_status()
    return session


@pytest.fixture()
def edit_repo(
    hf_api: HfApi, hf_endpoint: str, namespace: str, unique_name: str
) -> Iterator[EditRepo]:
    repo_id = f"{namespace}/{unique_name}-edit"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    try:
        hf_api.upload_file(
            path_or_fileobj=INITIAL_README.encode("utf-8"),
            path_in_repo="README.md",
            repo_id=repo_id,
            repo_type="model",
            commit_message="Add README",
        )
        hf_api.upload_file(
            path_or_fileobj=safetensors_file(),
            path_in_repo="model.safetensors",
            repo_id=repo_id,
            repo_type="model",
            commit_message="Add checkpoint",
        )
        ns, name = repo_id.split("/")
        yield EditRepo(
            repo_id=repo_id,
            ns=ns,
            name=name,
            endpoint=hf_endpoint,
            session=_login_session(hf_endpoint),
        )
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model")


def test_edit_updates_content_and_commit_message(edit_repo: EditRepo, hf_api: HfApi) -> None:
    before = hf_api.repo_info(repo_id=edit_repo.repo_id, repo_type="model")
    base_oid = edit_repo.root_oid("README.md")

    new_content = "# e2e web-edit\n\nedited from the web UI\n"
    resp = edit_repo.session.put(
        edit_repo.edit_url("README.md"),
        json={"content": new_content, "message": "Edit from the web UI", "base_oid": base_oid},
    )
    assert resp.status_code == 200, resp.text
    body = resp.json()
    assert body["oid"] != base_oid

    raw = edit_repo.session.get(
        edit_repo.url(f"/api/v1/raw/model/{edit_repo.ns}/{edit_repo.name}/main/README.md")
    ).json()
    assert raw["content"] == new_content

    # The commit landed: HEAD moved.
    after = hf_api.repo_info(repo_id=edit_repo.repo_id, repo_type="model")
    assert after.sha != before.sha


def test_stale_base_oid_is_rejected_with_409(edit_repo: EditRepo) -> None:
    stale_oid = edit_repo.root_oid("README.md")

    first = edit_repo.session.put(
        edit_repo.edit_url("README.md"),
        json={"content": "first edit\n", "message": "first", "base_oid": stale_oid},
    )
    assert first.status_code == 200, first.text

    # Re-sending the *original* base_oid should now conflict, since the
    # path has moved on.
    second = edit_repo.session.put(
        edit_repo.edit_url("README.md"),
        json={"content": "second edit\n", "message": "second", "base_oid": stale_oid},
    )
    assert second.status_code == 409, second.text


def test_noop_edit_keeps_same_oid(edit_repo: EditRepo) -> None:
    base_oid = edit_repo.root_oid("README.md")
    content = "# e2e web-edit\n\nsettled content\n"

    first = edit_repo.session.put(
        edit_repo.edit_url("README.md"),
        json={"content": content, "message": "settle", "base_oid": base_oid},
    )
    assert first.status_code == 200, first.text
    settled_oid = first.json()["oid"]

    # Re-saving identical content, with a fresh (now-current) base_oid,
    # should not mint a new blob.
    second = edit_repo.session.put(
        edit_repo.edit_url("README.md"),
        json={"content": content, "message": "no-op", "base_oid": settled_oid},
    )
    assert second.status_code == 200, second.text
    assert second.json()["oid"] == settled_oid


def test_lfs_path_edit_is_rejected(edit_repo: EditRepo) -> None:
    resp = edit_repo.session.put(
        edit_repo.edit_url("model.safetensors"),
        json={"content": "not a real checkpoint", "message": "nope"},
    )
    assert resp.status_code == 400


def test_new_file_created_without_base_oid(edit_repo: EditRepo) -> None:
    resp = edit_repo.session.put(
        edit_repo.edit_url("docs/new-file.md"),
        json={"content": "# brand new\n", "message": "Create a file"},
    )
    assert resp.status_code == 200, resp.text

    raw = edit_repo.session.get(
        edit_repo.url(f"/api/v1/raw/model/{edit_repo.ns}/{edit_repo.name}/main/docs/new-file.md")
    ).json()
    assert raw["content"] == "# brand new\n"


def test_existing_file_overwritten_without_base_oid(edit_repo: EditRepo) -> None:
    """An empty base_oid opts out of optimistic locking entirely: overwriting
    an existing file without one must succeed, not be mistaken for a
    "path must be absent" assertion (regression for the WAL precondition
    wiring)."""
    create = edit_repo.session.put(
        edit_repo.edit_url("docs/overwrite-me.md"),
        json={"content": "first\n", "message": "Create"},
    )
    assert create.status_code == 200, create.text

    overwrite = edit_repo.session.put(
        edit_repo.edit_url("docs/overwrite-me.md"),
        json={"content": "second\n", "message": "Overwrite without lock"},
    )
    assert overwrite.status_code == 200, overwrite.text

    raw = edit_repo.session.get(
        edit_repo.url(
            f"/api/v1/raw/model/{edit_repo.ns}/{edit_repo.name}/main/docs/overwrite-me.md"
        )
    ).json()
    assert raw["content"] == "second\n"


def test_commit_sha_as_rev_is_rejected(edit_repo: EditRepo) -> None:
    detail = edit_repo.session.get(
        edit_repo.url(f"/api/v1/repos/model/{edit_repo.ns}/{edit_repo.name}")
    ).json()
    head_sha = detail["repo"]["head_sha"]

    resp = edit_repo.session.put(
        edit_repo.edit_url("README.md", rev=head_sha),
        json={"content": "x", "message": "y"},
    )
    assert resp.status_code == 400


def test_unauthenticated_edit_is_rejected(edit_repo: EditRepo) -> None:
    anon = requests.Session()
    resp = anon.put(
        edit_repo.edit_url("README.md"),
        json={"content": "anon edit", "message": "x"},
    )
    assert resp.status_code == 401


def test_can_write_reflects_authentication(edit_repo: EditRepo) -> None:
    detail_url = edit_repo.url(f"/api/v1/repos/model/{edit_repo.ns}/{edit_repo.name}")

    as_owner = edit_repo.session.get(detail_url).json()["repo"]
    assert as_owner["can_write"] is True

    anon = requests.get(detail_url).json()["repo"]
    assert anon["can_write"] is False
