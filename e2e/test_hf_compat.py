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
from huggingface_hub.utils import HfHubHTTPError, RevisionNotFoundError


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
