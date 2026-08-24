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
from pathlib import Path

import pyarrow as pa
import pyarrow.parquet as pq
import pytest
import requests
from huggingface_hub import HfApi, HfFileSystem, RepoFile, RepoFolder
from huggingface_hub.utils import HfHubHTTPError, RepositoryNotFoundError, RevisionNotFoundError


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


def test_branch_lifecycle(hf_api: HfApi, namespace: str, unique_name: str) -> None:
    """`create_branch` / `delete_branch` against a repo created moments before.

    The status codes matter as much as the happy path here: `exist_ok=True`
    only swallows a 409, so a server answering anything else turns a tolerated
    duplicate into a raised exception.
    """
    repo_id = f"{namespace}/{unique_name}-branches"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    try:
        hf_api.create_branch(repo_id=repo_id, branch="experiment")

        refs = hf_api.list_repo_refs(repo_id=repo_id)
        names = {branch.name for branch in refs.branches}
        assert "experiment" in names
        assert "main" in names
        tips = {branch.name: branch.target_commit for branch in refs.branches}
        assert tips["experiment"] == tips["main"]

        # A duplicate is a 409, which exist_ok=True is defined to absorb.
        with pytest.raises(HfHubHTTPError):
            hf_api.create_branch(repo_id=repo_id, branch="experiment")
        hf_api.create_branch(repo_id=repo_id, branch="experiment", exist_ok=True)

        # A branch may start from an explicit revision, not just the tip.
        initial = hf_api.list_repo_commits(repo_id=repo_id)[-1]
        hf_api.create_branch(repo_id=repo_id, branch="from-initial", revision=initial.commit_id)
        tips = {b.name: b.target_commit for b in hf_api.list_repo_refs(repo_id=repo_id).branches}
        assert tips["from-initial"] == initial.commit_id

        # A revision that does not exist is a RevisionNotFoundError, not a 500.
        with pytest.raises(HfHubHTTPError):
            hf_api.create_branch(repo_id=repo_id, branch="nowhere", revision="no-such-revision")

        hf_api.delete_branch(repo_id=repo_id, branch="experiment")
        names = {branch.name for branch in hf_api.list_repo_refs(repo_id=repo_id).branches}
        assert "experiment" not in names

        # Deleting it twice, and deleting the default branch, both fail.
        with pytest.raises(HfHubHTTPError):
            hf_api.delete_branch(repo_id=repo_id, branch="experiment")
        with pytest.raises(HfHubHTTPError):
            hf_api.delete_branch(repo_id=repo_id, branch="main")
        assert "main" in {b.name for b in hf_api.list_repo_refs(repo_id=repo_id).branches}
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model")


def test_branch_with_a_slash_in_its_name(hf_api: HfApi, namespace: str, unique_name: str) -> None:
    """huggingface_hub percent-encodes the branch name (`quote(..., safe="")`).

    A server that routes on the decoded path would 404 here, and one that never
    unescapes would create a ref literally called `feature%2Fx`.
    """
    repo_id = f"{namespace}/{unique_name}-slashed"
    hf_api.create_repo(repo_id=repo_id, repo_type="dataset", private=False)
    try:
        hf_api.create_branch(repo_id=repo_id, repo_type="dataset", branch="feature/tokenizer")
        names = {
            b.name for b in hf_api.list_repo_refs(repo_id=repo_id, repo_type="dataset").branches
        }
        assert "feature/tokenizer" in names

        hf_api.delete_branch(repo_id=repo_id, repo_type="dataset", branch="feature/tokenizer")
        names = {
            b.name for b in hf_api.list_repo_refs(repo_id=repo_id, repo_type="dataset").branches
        }
        assert "feature/tokenizer" not in names
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="dataset")


def test_upload_download_and_list_on_a_branch_name_with_a_slash(
    hf_api: HfApi, namespace: str, unique_name: str
) -> None:
    """A slashed branch name must work through every read/write path, not just
    `create_branch` / `delete_branch` (covered above by
    `test_branch_with_a_slash_in_its_name`).

    `huggingface_hub` percent-encodes `revision` wherever it appears in a URL
    (`quote(revision, safe="")`), so `feature/tokenizer-update` is sent as
    `feature%2Ftokenizer-update` by `upload_file` / `hf_hub_download` /
    `list_repo_files` / `list_repo_tree` / `repo_info` alike. A server that
    only unescapes the branch/tag routes -- or that decodes `%2F` back into a
    path separator before routing -- would 404 or silently answer with the
    wrong ref for any of these. This test also confirms the write actually
    landed on the feature branch and not on `main`.
    """
    from huggingface_hub import hf_hub_download

    repo_id = f"{namespace}/{unique_name}-slash-rw"
    hf_api.create_repo(repo_id=repo_id, repo_type="dataset", private=False)
    try:
        branch = "feature/tokenizer-update"
        hf_api.create_branch(repo_id=repo_id, repo_type="dataset", branch=branch)

        payload = b"content that only exists on the feature branch\n"
        hf_api.upload_file(
            path_or_fileobj=payload,
            path_in_repo="branch-only.txt",
            repo_id=repo_id,
            repo_type="dataset",
            revision=branch,
            commit_message="Add a file on the feature branch",
        )

        # --- download: byte-for-byte round trip through the slashed revision
        downloaded = hf_hub_download(
            repo_id=repo_id, repo_type="dataset", filename="branch-only.txt", revision=branch
        )
        with open(downloaded, "rb") as f:
            assert f.read() == payload

        # --- listing endpoints see the file at that revision
        files = hf_api.list_repo_files(repo_id=repo_id, repo_type="dataset", revision=branch)
        assert "branch-only.txt" in files

        tree_paths = {
            entry.path
            for entry in hf_api.list_repo_tree(
                repo_id=repo_id, repo_type="dataset", revision=branch
            )
        }
        assert "branch-only.txt" in tree_paths

        # --- repo_info resolves the slashed revision to the branch's own tip,
        # not to whatever `main` happens to point at.
        branch_tip = {
            b.name: b.target_commit
            for b in hf_api.list_repo_refs(repo_id=repo_id, repo_type="dataset").branches
        }[branch]
        info = hf_api.repo_info(repo_id=repo_id, repo_type="dataset", revision=branch)
        assert info.sha == branch_tip

        # --- and `main` was never touched by any of the above.
        main_files = hf_api.list_repo_files(repo_id=repo_id, repo_type="dataset", revision="main")
        assert "branch-only.txt" not in main_files
        main_info = hf_api.repo_info(repo_id=repo_id, repo_type="dataset", revision="main")
        assert main_info.sha != info.sha
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="dataset")


def test_tag_lifecycle(hf_api: HfApi, namespace: str, unique_name: str) -> None:
    """`create_tag` / `delete_tag`, both lightweight and annotated.

    Note the asymmetry in huggingface_hub's own URLs: create puts the *revision*
    in the path and the tag name in the body, delete puts the tag name in the
    path.
    """
    repo_id = f"{namespace}/{unique_name}-tags"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    try:
        hf_api.create_tag(repo_id=repo_id, tag="v1.0")
        hf_api.create_tag(repo_id=repo_id, tag="v1.1", tag_message="the second release")

        tags = {tag.name for tag in hf_api.list_repo_refs(repo_id=repo_id).tags}
        assert {"v1.0", "v1.1"} <= tags

        # An annotated tag still resolves to the commit it tags everywhere a
        # revision is accepted.
        head = hf_api.repo_info(repo_id=repo_id).sha
        assert hf_api.repo_info(repo_id=repo_id, revision="v1.1").sha == head
        assert hf_api.repo_info(repo_id=repo_id, revision="v1.0").sha == head

        with pytest.raises(HfHubHTTPError):
            hf_api.create_tag(repo_id=repo_id, tag="v1.0")
        hf_api.create_tag(repo_id=repo_id, tag="v1.0", exist_ok=True)

        with pytest.raises(HfHubHTTPError):
            hf_api.create_tag(repo_id=repo_id, tag="v2.0", revision="no-such-revision")

        hf_api.delete_tag(repo_id=repo_id, tag="v1.0")
        tags = {tag.name for tag in hf_api.list_repo_refs(repo_id=repo_id).tags}
        assert "v1.0" not in tags
        assert "v1.1" in tags

        with pytest.raises(HfHubHTTPError):
            hf_api.delete_tag(repo_id=repo_id, tag="v1.0")
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model")


def test_list_repo_commits(hf_api: HfApi, namespace: str, unique_name: str) -> None:
    """`list_repo_commits` must fill in every GitCommitInfo field.

    The client indexes `id`, `authors`, `date`, `title` and `message` directly
    -- a missing key is a KeyError -- parses `date` with a format that only
    accepts a trailing "Z", and reads `user` out of each author *object*.
    """
    repo_id = f"{namespace}/{unique_name}-commits"
    hf_api.create_repo(repo_id=repo_id, repo_type="dataset", private=False)
    try:
        hf_api.upload_file(
            path_or_fileobj=b"first\n",
            path_in_repo="notes.txt",
            repo_id=repo_id,
            repo_type="dataset",
            commit_message="Add notes",
            commit_description="A longer explanation of the change.",
        )

        commits = hf_api.list_repo_commits(repo_id=repo_id, repo_type="dataset")
        assert len(commits) >= 2  # the initial commit plus the upload

        # Newest first, so the upload is at the head and the initial commit is
        # the last element.
        newest = commits[0]
        assert newest.title == "Add notes"
        assert "A longer explanation of the change." in newest.message
        assert len(newest.commit_id) == 40
        assert newest.authors  # parsed out of the `authors` array of objects
        assert newest.created_at is not None
        assert newest.created_at.tzinfo is not None

        # Every commit is reachable from the default branch, and asking for it
        # explicitly gives the same answer.
        by_revision = hf_api.list_repo_commits(
            repo_id=repo_id, repo_type="dataset", revision="main"
        )
        assert [c.commit_id for c in by_revision] == [c.commit_id for c in commits]

        with pytest.raises(HfHubHTTPError):
            hf_api.list_repo_commits(
                repo_id=repo_id, repo_type="dataset", revision="no-such-revision"
            )
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="dataset")


def test_list_repo_commits_rejects_a_bogus_cursor(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, namespace: str, unique_name: str
) -> None:
    """A cursor naming no commit is the caller's mistake, not a bad revision.

    `list_repo_commits` follows the server's own `Link` header, so a bogus
    `after` is only reachable by hand -- hence plain `requests` here. What is
    being pinned is that the answer is a 400 and *not* a 404 with
    `X-Error-Code: RevisionNotFound`: the revision resolved fine, and telling
    the client otherwise sends it hunting for a `revision=` problem it does not
    have.
    """
    repo_id = f"{namespace}/{unique_name}-cursor"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    try:
        url = f"{hf_endpoint}/api/models/{repo_id}/commits/main"
        headers = {"Authorization": f"Bearer {hf_token}"}

        # Sanity check: the same URL without a cursor is a normal 200, so the
        # assertions below are about the cursor and nothing else.
        ok = requests.get(url, headers=headers, timeout=10)
        assert ok.status_code == 200, ok.text
        assert isinstance(ok.json(), list)

        bogus = requests.get(url, params={"after": "a" * 40}, headers=headers, timeout=10)
        assert bogus.status_code == 400, bogus.text
        assert bogus.headers.get("X-Error-Code") != "RevisionNotFound"
        assert bogus.json()["error"]["type"] == "bad_request"

        # A cursor that is not a full hash at all is rejected the same way.
        malformed = requests.get(url, params={"after": "not-a-hash"}, headers=headers, timeout=10)
        assert malformed.status_code == 400, malformed.text
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model")


def test_list_repo_commits_pages_with_a_link_header(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, namespace: str, unique_name: str
) -> None:
    """The cursor the server hands out must be one it accepts back.

    This is the other half of the test above: the 400 must not be rejecting
    legitimate pagination.
    """
    repo_id = f"{namespace}/{unique_name}-paging"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    try:
        for i in range(3):
            hf_api.upload_file(
                path_or_fileobj=f"revision {i}\n".encode(),
                path_in_repo="notes.txt",
                repo_id=repo_id,
                commit_message=f"Edit {i}",
            )

        url = f"{hf_endpoint}/api/models/{repo_id}/commits/main"
        headers = {"Authorization": f"Bearer {hf_token}"}
        first = requests.get(url, params={"limit": 2}, headers=headers, timeout=10)
        assert first.status_code == 200, first.text
        assert len(first.json()) == 2

        # requests parses the Link header the same way huggingface_hub's
        # paginate() does.
        next_url = first.links["next"]["url"]
        second = requests.get(next_url, headers=headers, timeout=10)
        assert second.status_code == 200, second.text
        assert second.json(), "the server's own cursor returned an empty page"

        first_ids = {c["id"] for c in first.json()}
        assert not (first_ids & {c["id"] for c in second.json()})
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model")


def test_revision_not_found_for_repo_with_commits(
    hf_api: HfApi, namespace: str, unique_name: str, tmp_path
) -> None:
    """An unknown `revision` on a repo that has commits must look not-found.

    repo-info / tree / paths-info used to treat any unrecognized `{rev}` as if
    it resolved to the default branch (or answered empty), so
    `huggingface_hub` never raised `RevisionNotFoundError` -- `revision_exists`
    incorrectly returned `True`, `list_repo_files` silently returned `[]`
    instead of raising, and `snapshot_download` would quietly materialize an
    empty (or wrong) snapshot instead of failing. The `X-Error-Code:
    RevisionNotFound` response header is what makes `huggingface_hub` raise
    the specific error instead of treating the call as a success.
    """
    repo_id = f"{namespace}/{unique_name}-revs"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    try:
        hf_api.upload_file(
            path_or_fileobj=b"hello\n",
            path_in_repo="notes.txt",
            repo_id=repo_id,
            repo_type="model",
            commit_message="Add notes",
        )
        commit_sha = hf_api.list_repo_commits(repo_id=repo_id, repo_type="model")[0].commit_id
        hf_api.create_branch(repo_id=repo_id, repo_type="model", branch="feature")
        hf_api.create_tag(repo_id=repo_id, repo_type="model", tag="v1.0")

        # --- the bug: an unrecognized revision must not look like success -
        assert (
            hf_api.revision_exists(repo_id=repo_id, revision="no-such-rev", repo_type="model")
            is False
        )

        with pytest.raises(RevisionNotFoundError):
            hf_api.list_repo_files(repo_id=repo_id, repo_type="model", revision="no-such-rev")

        with pytest.raises(RevisionNotFoundError):
            list(hf_api.list_repo_tree(repo_id=repo_id, repo_type="model", revision="no-such-rev"))

        with pytest.raises(RevisionNotFoundError):
            hf_api.get_paths_info(
                repo_id=repo_id, repo_type="model", revision="no-such-rev", paths=["notes.txt"]
            )

        with pytest.raises(RevisionNotFoundError):
            hf_api.snapshot_download(
                repo_id=repo_id,
                repo_type="model",
                revision="no-such-rev",
                local_dir=tmp_path / "snapshot",
            )

        # --- regression: real branches / tags / commit SHAs keep working --
        for revision in ("main", "feature", "v1.0", commit_sha):
            assert (
                hf_api.revision_exists(repo_id=repo_id, revision=revision, repo_type="model")
                is True
            )
            files = hf_api.list_repo_files(repo_id=repo_id, repo_type="model", revision=revision)
            assert "notes.txt" in files
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model")


def test_get_paths_info_returns_entries_for_real_paths(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, namespace: str, unique_name: str
) -> None:
    """`get_paths_info` must answer with the paths that really are there.

    `HfApi.get_paths_info` posts its batch as `data={"paths": [...],
    "expand": ...}` -- *form-encoded*, not JSON. The server parsed only JSON
    and swallowed the failure, so every real request decoded to zero paths and
    came back `200 []`: an endpoint that could only ever answer "none of these
    exist". This suite never noticed, because the only thing it asserted about
    paths-info was that a bad revision raises -- which it did, for the wrong
    reason.

    `HfFileSystem.info()` / `.exists()` are the fallout that matters: they turn
    an empty answer into a `FileNotFoundError`, which is what breaks `datasets`
    and `pandas.read_parquet("hf://...")`. `CommitOperationCopy` cannot resolve
    its source either.
    """
    repo_id = f"{namespace}/{unique_name}-paths-info"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    try:
        hf_api.upload_file(
            path_or_fileobj=b"hello\n",
            path_in_repo="notes.txt",
            repo_id=repo_id,
            repo_type="model",
            commit_message="Add notes",
        )
        hf_api.upload_file(
            path_or_fileobj=b"a,b\n1,2\n",
            path_in_repo="data/train.csv",
            repo_id=repo_id,
            repo_type="model",
            commit_message="Add data",
        )

        # --- the bug: a batch of paths that exist must come back non-empty -
        infos = hf_api.get_paths_info(
            repo_id=repo_id, repo_type="model", paths=["notes.txt", "data"]
        )
        by_path = {item.path: item for item in infos}
        assert set(by_path) == {"notes.txt", "data"}, f"paths-info returned {by_path!r}"
        assert isinstance(by_path["notes.txt"], RepoFile)
        assert by_path["notes.txt"].size == len(b"hello\n")
        assert by_path["notes.txt"].blob_id
        assert isinstance(by_path["data"], RepoFolder)

        # A bare string is wrapped in a list by the client, so it travels as a
        # one-element form field rather than a JSON array.
        only = hf_api.get_paths_info(repo_id=repo_id, repo_type="model", paths="notes.txt")
        assert [item.path for item in only] == ["notes.txt"]

        # Absent paths are left out of the answer rather than reported: what
        # must not happen is the whole batch coming back empty because of one.
        mixed = hf_api.get_paths_info(
            repo_id=repo_id, repo_type="model", paths=["notes.txt", "no-such-file.txt"]
        )
        assert [item.path for item in mixed] == ["notes.txt"]

        # Naming a revision explicitly takes the same path through the client.
        on_main = hf_api.get_paths_info(
            repo_id=repo_id, repo_type="model", revision="main", paths=["data/train.csv"]
        )
        assert [item.path for item in on_main] == ["data/train.csv"]

        # --- the caller that depends on it: HfFileSystem.exists / .info ----
        fs = HfFileSystem(endpoint=hf_endpoint, token=hf_token)
        assert fs.exists(f"{repo_id}/notes.txt")
        assert not fs.exists(f"{repo_id}/no-such-file.txt")
        assert fs.info(f"{repo_id}/notes.txt")["size"] == len(b"hello\n")
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model")


def test_repo_info_succeeds_right_after_create_repo(
    hf_api: HfApi, namespace: str, unique_name: str
) -> None:
    """`create_repo()` immediately followed by `repo_info()` must stay a 200.

    `create_repo` always writes an initial commit synchronously before
    returning (see `createRepo` in backend/internal/api/repos.go), so a
    genuinely 0-commit repository is never observable through this suite --
    but the invariant that matters to a client is exactly this boundary: the
    revision-not-found check added for the bug above must not turn the
    ordinary create -> read flow into a 404, whatever revision string
    `repo_info()` happens to be called with by default.
    """
    repo_id = f"{namespace}/{unique_name}-fresh"
    hf_api.create_repo(repo_id=repo_id, repo_type="dataset", private=False)
    try:
        info = hf_api.repo_info(repo_id=repo_id, repo_type="dataset")
        assert info.id == repo_id

        info_main = hf_api.repo_info(repo_id=repo_id, repo_type="dataset", revision="main")
        assert info_main.sha == info.sha

        assert list(hf_api.list_repo_tree(repo_id=repo_id, repo_type="dataset"))
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="dataset")


# --- preupload oid / diff-upload skipping ------------------------------------
#
# `POST .../preupload/{rev}` reports, for every file huggingface_hub is about
# to upload, whether the repository already has a file at that path with the
# exact same content -- `oid` in the response is the git blob sha1 for
# `uploadMode: "regular"` and the content sha256 for `uploadMode: "lfs"`, or
# `null` when there is nothing to compare against (docs/dev/api-contract.md).
# huggingface_hub stores this as `CommitOperationAdd._remote_oid`, compares it
# against the locally computed oid, and drops any operation whose oid matches
# from the commit -- if every operation is dropped, it never POSTs to
# /commit at all, so a byte-identical re-upload must not grow the commit log.
# The tests below exercise both directions: nothing changed -> no commit, and
# exactly one file changed -> exactly one commit whose *content* reflects the
# change (not just its existence), since silently skipping a file that did
# change would be silent data loss.


def _write_folder(root: Path, *, note: bytes, weights: bytes) -> None:
    """Populate `root` with one regular file and one LFS-tracked file.

    `notes.txt` is a plain-text file, so preupload reports it with
    `uploadMode: "regular"`. `weights.bin` matches the `*.bin` pattern in the
    default `.gitattributes` (backend/internal/gitrepo/gitattributes.go), so
    it is always `uploadMode: "lfs"` regardless of its size.
    """
    root.mkdir(parents=True, exist_ok=True)
    (root / "notes.txt").write_bytes(note)
    (root / "weights.bin").write_bytes(weights)


def test_reupload_of_unchanged_folder_adds_no_commit(
    hf_api: HfApi, namespace: str, unique_name: str, tmp_path
) -> None:
    """Re-uploading a folder with byte-identical content creates no commit.

    Before the preupload oid fix, this endpoint always reported `oid: null`,
    so huggingface_hub could never recognize an already-present file as
    unchanged and every re-upload of an unmodified folder created a fresh,
    empty-diff commit.
    """
    repo_id = f"{namespace}/{unique_name}-noop-reupload"
    hf_api.create_repo(repo_id=repo_id, repo_type="dataset", private=False)
    try:
        src = tmp_path / "folder"
        _write_folder(src, note=b"hello from e2e\n", weights=b"\x00\x01lfs-weights-v1" * 100)

        hf_api.upload_folder(
            folder_path=str(src),
            repo_id=repo_id,
            repo_type="dataset",
            commit_message="Initial upload",
        )
        commits_after_first = hf_api.list_repo_commits(repo_id=repo_id, repo_type="dataset")

        # Same folder, same bytes: nothing to commit.
        hf_api.upload_folder(
            folder_path=str(src),
            repo_id=repo_id,
            repo_type="dataset",
            commit_message="Re-upload, unchanged",
        )
        commits_after_second = hf_api.list_repo_commits(repo_id=repo_id, repo_type="dataset")

        assert len(commits_after_second) == len(commits_after_first)
        assert commits_after_second[0].commit_id == commits_after_first[0].commit_id
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="dataset")


def test_reupload_after_changing_one_regular_file(
    hf_api: HfApi, namespace: str, unique_name: str, tmp_path
) -> None:
    """A modified regular file is re-uploaded; an unmodified sibling is not.

    (b) below is the safety-critical assertion: naively treating every file
    in the folder as "unchanged, skip it" would also skip a file that
    genuinely changed, which is silent data loss on the server.
    """
    from huggingface_hub import hf_hub_download

    repo_id = f"{namespace}/{unique_name}-regular-reupload"
    hf_api.create_repo(repo_id=repo_id, repo_type="dataset", private=False)
    try:
        src = tmp_path / "folder"
        _write_folder(src, note=b"version 1\n", weights=b"\x00\x01lfs-weights-v1" * 100)
        hf_api.upload_folder(
            folder_path=str(src),
            repo_id=repo_id,
            repo_type="dataset",
            commit_message="Initial upload",
        )
        commits_before = hf_api.list_repo_commits(repo_id=repo_id, repo_type="dataset")

        # Only notes.txt changes; weights.bin is rewritten with identical bytes.
        (src / "notes.txt").write_bytes(b"version 2 -- updated\n")
        (src / "weights.bin").write_bytes(b"\x00\x01lfs-weights-v1" * 100)

        hf_api.upload_folder(
            folder_path=str(src),
            repo_id=repo_id,
            repo_type="dataset",
            commit_message="Update notes only",
        )

        # (a) exactly one new commit for the one real change.
        commits_after = hf_api.list_repo_commits(repo_id=repo_id, repo_type="dataset")
        assert len(commits_after) == len(commits_before) + 1

        # (b) the changed file's new content actually landed on the server.
        downloaded_notes = hf_hub_download(
            repo_id=repo_id, repo_type="dataset", filename="notes.txt"
        )
        with open(downloaded_notes, "rb") as f:
            assert f.read() == b"version 2 -- updated\n"

        # (c) the untouched file's content is exactly as it was.
        downloaded_weights = hf_hub_download(
            repo_id=repo_id, repo_type="dataset", filename="weights.bin"
        )
        with open(downloaded_weights, "rb") as f:
            assert f.read() == b"\x00\x01lfs-weights-v1" * 100
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="dataset")


def test_reupload_after_changing_lfs_file(
    hf_api: HfApi, namespace: str, unique_name: str, tmp_path
) -> None:
    """Same guarantee as `test_reupload_after_changing_one_regular_file`, but
    for a file that goes through the LFS path.

    `weights.bin` is LFS-tracked via `.gitattributes`, so its preupload `oid`
    is a content sha256 rather than a git blob sha1 -- this exercises that
    branch of the fix independently of the regular-file case above, covering
    both directions (unchanged-skip via the shared setup, changed-reupload
    here) for `uploadMode: "lfs"`.
    """
    from huggingface_hub import hf_hub_download

    repo_id = f"{namespace}/{unique_name}-lfs-reupload"
    hf_api.create_repo(repo_id=repo_id, repo_type="dataset", private=False)
    try:
        src = tmp_path / "folder"
        _write_folder(src, note=b"stable notes\n", weights=b"\x00\x01lfs-weights-v1" * 100)
        hf_api.upload_folder(
            folder_path=str(src),
            repo_id=repo_id,
            repo_type="dataset",
            commit_message="Initial upload",
        )
        commits_before = hf_api.list_repo_commits(repo_id=repo_id, repo_type="dataset")

        # Only the LFS file changes this time; notes.txt is rewritten with
        # identical bytes.
        (src / "notes.txt").write_bytes(b"stable notes\n")
        (src / "weights.bin").write_bytes(b"\xff\xfelfs-weights-v2" * 100)

        hf_api.upload_folder(
            folder_path=str(src),
            repo_id=repo_id,
            repo_type="dataset",
            commit_message="Update weights only",
        )

        commits_after = hf_api.list_repo_commits(repo_id=repo_id, repo_type="dataset")
        assert len(commits_after) == len(commits_before) + 1

        downloaded_weights = hf_hub_download(
            repo_id=repo_id, repo_type="dataset", filename="weights.bin"
        )
        with open(downloaded_weights, "rb") as f:
            assert f.read() == b"\xff\xfelfs-weights-v2" * 100

        downloaded_notes = hf_hub_download(
            repo_id=repo_id, repo_type="dataset", filename="notes.txt"
        )
        with open(downloaded_notes, "rb") as f:
            assert f.read() == b"stable notes\n"
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="dataset")


# --- repo_exists / file_exists / revision_exists on a missing repository -----


def test_repo_exists_and_friends_return_false_for_a_missing_repo(
    hf_api: HfApi, unique_name: str, namespace: str
) -> None:
    """`repo_exists` / `file_exists` / `revision_exists` must answer `False`,
    not raise, for a repository that was never created.

    `huggingface_hub` implements all three as a `try/except RepositoryNotFoundError`
    around a plain read call (`repo_info` / `hf_hub_url` HEAD / etc.) -- see
    `HfApi.repo_exists` in the installed `huggingface_hub` package. That
    exception is only raised when the response carries
    `X-Error-Code: RepoNotFound` (or a 401); a bare 404 with no header comes
    back as a generic `HfHubHTTPError`, which none of these three catch, so
    before the fix every one of them raised instead of returning `False`.
    """
    repo_id = f"{namespace}/{unique_name}-never-created"

    assert hf_api.repo_exists(repo_id=repo_id, repo_type="model") is False
    assert hf_api.repo_exists(repo_id=repo_id, repo_type="dataset") is False
    assert hf_api.file_exists(repo_id=repo_id, filename="README.md", repo_type="model") is False
    assert hf_api.revision_exists(repo_id=repo_id, revision="main", repo_type="model") is False

    # repo_info itself still raises -- repo_exists() et al. are the layer that
    # translates "not found" into a boolean, not a change to the read itself.
    with pytest.raises(RepositoryNotFoundError):
        hf_api.repo_info(repo_id=repo_id, repo_type="model")


# --- create_pr is not implemented, and must fail rather than silently commit -


def test_upload_file_with_create_pr_is_rejected(
    hf_api: HfApi, namespace: str, unique_name: str
) -> None:
    """`create_pr=True` must fail outright rather than silently landing on `main`.

    Pull requests are not implemented server-side, so a commit that asks for
    one has to be rejected -- quietly accepting it and committing straight to
    the default branch instead would be worse than an outright error, since it
    would surprise a caller who expects a review step before their change
    becomes visible. The exact status code / exception subtype is an
    implementation detail (see docs/dev/api-contract.md); what this test pins
    down is that the call does not succeed and that nothing was committed
    anywhere as a side effect of the attempt.
    """
    repo_id = f"{namespace}/{unique_name}-create-pr"
    hf_api.create_repo(repo_id=repo_id, repo_type="dataset", private=False)
    try:
        commits_before = hf_api.list_repo_commits(repo_id=repo_id, repo_type="dataset")

        with pytest.raises(HfHubHTTPError):
            hf_api.upload_file(
                path_or_fileobj=b"should never land anywhere\n",
                path_in_repo="pr-only.txt",
                repo_id=repo_id,
                repo_type="dataset",
                commit_message="Attempt a PR",
                create_pr=True,
            )

        # Nothing was silently committed to main as a side effect of the
        # rejected attempt.
        commits_after = hf_api.list_repo_commits(repo_id=repo_id, repo_type="dataset")
        assert len(commits_after) == len(commits_before)
        files = hf_api.list_repo_files(repo_id=repo_id, repo_type="dataset")
        assert "pr-only.txt" not in files
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="dataset")


# --- error responses must carry a message a human (or huggingface_hub) can read


def test_error_response_carries_a_readable_x_error_message_header(
    hf_endpoint: str, hf_token: str, namespace: str, unique_name: str
) -> None:
    """Every error response must carry `X-Error-Message` equal to the body's
    `error.message`.

    `huggingface_hub.utils._http.hf_raise_for_status` reads the *header*
    before it ever looks at the body, and thinkingface spells its error body
    as an object (`{"error": {"message": "...", "type": "..."}}`) rather than
    upstream HF's plain string -- without the header, the only text
    `huggingface_hub` can pull out of the body is that object's Python
    `repr()` (`"{'message': '...', 'type': '...'}"`), which is what callers
    used to see instead of the actual sentence. Checked directly over HTTP
    rather than through `huggingface_hub`, since the client folds the header
    text and the body text together into a single exception message and the
    result doesn't cleanly isolate one from the other.
    """
    repo_id = f"{namespace}/{unique_name}-error-message"
    resp = requests.get(
        f"{hf_endpoint}/api/models/{repo_id}",
        headers={"Authorization": f"Bearer {hf_token}"},
        timeout=10,
    )
    assert resp.status_code == 404
    assert resp.headers.get("X-Error-Code") == "RepoNotFound"

    body = resp.json()
    assert resp.headers.get("X-Error-Message") == body["error"]["message"]
