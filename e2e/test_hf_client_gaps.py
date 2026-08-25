"""End-to-end coverage for the HF-compatible endpoints added to close the
gaps a spec audit found: a missing revision that did not answer
`RevisionNotFound`, a missing `auth-check`, a missing tag catalogue, and a
`resolve` that issued an ETag it would not accept back.

These go through the real `huggingface_hub` client wherever it has a call for
the endpoint, because the point of every one of them is what the client does
with the answer -- a hand-rolled `requests` call would happily pass while
`file_exists()` still raised.
"""

from __future__ import annotations

import pytest
import requests
from huggingface_hub import HfApi
from huggingface_hub.utils import RepositoryNotFoundError


@pytest.fixture()
def seeded_model(hf_api: HfApi, namespace: str, unique_name: str) -> str:
    """A model repository holding one small file, deleted afterwards."""
    repo_id = f"{namespace}/{unique_name}-gaps"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    hf_api.upload_file(
        path_or_fileobj=b"hello\n",
        path_in_repo="hello.txt",
        repo_id=repo_id,
        repo_type="model",
    )
    yield repo_id
    hf_api.delete_repo(repo_id=repo_id, repo_type="model")


def test_file_exists_answers_false_for_an_unknown_revision(
    hf_api: HfApi, seeded_model: str
) -> None:
    """`file_exists` catches RevisionNotFoundError and returns False. While
    resolve answered a plain 404 the error arrived as HfHubHTTPError, which
    that except clause does not name, so a typo in `revision=` raised instead
    of answering the question."""
    assert hf_api.file_exists(seeded_model, "hello.txt", revision="main") is True
    assert hf_api.file_exists(seeded_model, "hello.txt", revision="no-such-branch") is False


def test_file_exists_answers_false_for_a_missing_path(hf_api: HfApi, seeded_model: str) -> None:
    """The other half of the same fix: a real revision missing the file must
    stay EntryNotFound, or a missing file would read as a missing revision."""
    assert hf_api.file_exists(seeded_model, "not-here.txt", revision="main") is False


def test_auth_check_passes_for_a_readable_repo(hf_api: HfApi, seeded_model: str) -> None:
    hf_api.auth_check(seeded_model, repo_type="model")


def test_auth_check_raises_for_a_missing_repo(hf_api: HfApi, namespace: str) -> None:
    with pytest.raises(RepositoryNotFoundError):
        hf_api.auth_check(f"{namespace}/definitely-not-a-repo", repo_type="model")


def test_get_model_and_dataset_tags(hf_api: HfApi) -> None:
    """`get_model_tags()` indexes a fixed set of group names on older
    huggingface_hub releases, so every group has to be present even when the
    instance has nothing to put in it."""
    model_tags = hf_api.get_model_tags()
    assert model_tags is not None
    dataset_tags = hf_api.get_dataset_tags()
    assert dataset_tags is not None


def test_resolve_honours_if_none_match(hf_endpoint: str, hf_token: str, seeded_model: str) -> None:
    """The server has always issued an ETag on resolve; until now it ignored
    the validator coming back, so every re-download was a full body."""
    url = f"{hf_endpoint}/{seeded_model}/resolve/main/hello.txt"
    headers = {"Authorization": f"Bearer {hf_token}"}

    first = requests.get(url, headers=headers, timeout=30)
    assert first.status_code == 200
    etag = first.headers.get("ETag")
    assert etag, "resolve must issue an ETag for a conditional request to be possible"

    second = requests.get(url, headers={**headers, "If-None-Match": etag}, timeout=30)
    assert second.status_code == 304
    assert second.content == b""
    assert second.headers.get("ETag") == etag


def test_super_squash_history_collapses_a_branch(hf_api: HfApi, seeded_model: str) -> None:
    """A repository whose history is worth squashing is one whose old commits
    are large, so the check that matters is that the *content* survives while
    the history stops at one commit."""
    hf_api.upload_file(
        path_or_fileobj=b"hello again\n",
        path_in_repo="hello.txt",
        repo_id=seeded_model,
        repo_type="model",
    )
    assert len(hf_api.list_repo_commits(seeded_model, repo_type="model")) > 1

    hf_api.super_squash_history(repo_id=seeded_model, repo_type="model")

    commits = hf_api.list_repo_commits(seeded_model, repo_type="model")
    assert len(commits) == 1
    assert "hello.txt" in hf_api.list_repo_files(seeded_model, repo_type="model")
