"""End-to-end cover for the HF-compatible parameters that used to be ignored.

The server previously accepted `sort`, `filter` and `parent_commit` and then
did something else, which is the failure mode this suite exists to catch: a
caller cannot tell a silently-different answer from a correct one. The
listing cases in particular guard a real regression risk in the other
direction, since refusing too much is just as incompatible as ignoring too
much -- `list_models(sort="downloads")` with no explicit direction has to keep
working, because that is how the call is almost always written.

Requires a running server; see e2e/README.md.
"""

from __future__ import annotations

import pytest
from huggingface_hub import CommitOperationAdd, CommitOperationDelete, HfApi
from huggingface_hub.utils import HfHubHTTPError


@pytest.fixture(scope="module")
def listing_repos(hf_api: HfApi, namespace: str) -> list[str]:
    """Three model repositories with distinguishable cards, left behind for
    the module. Tags come from the README front matter, which is what the
    server indexes into the facets `filter=` searches."""
    made: list[str] = []
    for i, tags in enumerate([["e2e-listing", "alpha"], ["e2e-listing", "beta"], ["e2e-listing"]]):
        repo_id = f"{namespace}/e2e-listing-{i}"
        hf_api.create_repo(repo_id=repo_id, repo_type="model", exist_ok=True)
        card = "---\ntags:\n" + "".join(f"  - {t}\n" for t in tags) + "---\n\n# listing fixture\n"
        hf_api.upload_file(
            path_or_fileobj=card.encode(),
            path_in_repo="README.md",
            repo_id=repo_id,
            repo_type="model",
        )
        made.append(repo_id)
    yield made
    for repo_id in made:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model", missing_ok=True)


def test_list_models_sorted_by_downloads_without_a_direction(
    hf_api: HfApi, listing_repos: list[str]
) -> None:
    """`sort="downloads"` alone must return results.

    This is the single most common way the call is written, and the server
    briefly answered it with a 400 demanding an explicit direction=-1.
    """
    got = list(hf_api.list_models(sort="downloads", limit=5))
    assert got, "sort=downloads returned nothing"


def test_list_models_sort_variants_all_return_results(
    hf_api: HfApi, listing_repos: list[str]
) -> None:
    """Every sort key this server accepts has to come back with results.

    Note there is no `direction` case here: `HfApi.list_models` does not take
    one, so `sort=` alone is the *only* way this client can ask for an order.
    That is exactly why requiring an explicit `direction=-1` was unreachable
    rather than merely strict.
    """
    for sort in ["downloads", "lastModified", "createdAt"]:
        assert list(hf_api.list_models(sort=sort, limit=5)), f"sort={sort} returned nothing"


def test_list_models_filter_narrows_to_the_tag(hf_api: HfApi, listing_repos: list[str]) -> None:
    tagged = {m.id for m in hf_api.list_models(filter="e2e-listing", limit=100)}
    assert set(listing_repos) <= tagged, "filter dropped repositories carrying the tag"

    alpha = {m.id for m in hf_api.list_models(filter="alpha", limit=100)}
    assert listing_repos[0] in alpha
    assert listing_repos[1] not in alpha, "filter returned a repository without the tag"


def test_list_models_pages_past_the_default_limit(hf_api: HfApi, namespace: str) -> None:
    """The listing used to stop at the server's page size with no Link header,
    so paginate() saw the 31st repository as nonexistent -- silent truncation
    that reads exactly like "there are no more".

    This creates its own repositories rather than skipping on a small
    instance: a case that skips whenever the fixture data is thin is a case
    that never runs.
    """
    page_size = 30
    made = [f"{namespace}/e2e-paging-{i:02d}" for i in range(page_size + 2)]
    for repo_id in made:
        hf_api.create_repo(repo_id=repo_id, repo_type="model", exist_ok=True)
    try:
        walked = [m.id for m in hf_api.list_models(limit=page_size + 2)]
        assert len(walked) > page_size, (
            f"asked for {page_size + 2} and got {len(walked)}: the listing truncated "
            "at its page size instead of offering a next page"
        )
        assert len(walked) == len(set(walked)), "paging repeated a repository across pages"
    finally:
        for repo_id in made:
            hf_api.delete_repo(repo_id=repo_id, repo_type="model", missing_ok=True)


def test_list_models_refuses_a_metric_it_does_not_track(hf_api: HfApi) -> None:
    """Sorting by likes must not come back as the default order dressed up as
    a ranking."""
    with pytest.raises(HfHubHTTPError) as excinfo:
        list(hf_api.list_models(sort="likes", limit=5))
    assert excinfo.value.response.status_code == 400


def test_create_commit_accepts_a_matching_parent(
    hf_api: HfApi, namespace: str, unique_name: str
) -> None:
    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", exist_ok=True)
    try:
        head = hf_api.list_repo_commits(repo_id=repo_id, repo_type="model")[0].commit_id
        hf_api.create_commit(
            repo_id=repo_id,
            repo_type="model",
            operations=[CommitOperationAdd(path_in_repo="a.txt", path_or_fileobj=b"first\n")],
            commit_message="with the right parent",
            parent_commit=head,
        )
        assert "a.txt" in hf_api.list_repo_files(repo_id=repo_id, repo_type="model")
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model", missing_ok=True)


def test_create_commit_rejects_a_stale_parent(
    hf_api: HfApi, namespace: str, unique_name: str
) -> None:
    """The whole point of parent_commit: a caller that read the branch, lost
    the race, and committed anyway used to get a 200."""
    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", exist_ok=True)
    try:
        stale = hf_api.list_repo_commits(repo_id=repo_id, repo_type="model")[0].commit_id
        # Somebody else moves the branch.
        hf_api.upload_file(
            path_or_fileobj=b"theirs\n",
            path_in_repo="theirs.txt",
            repo_id=repo_id,
            repo_type="model",
        )
        with pytest.raises(HfHubHTTPError) as excinfo:
            hf_api.create_commit(
                repo_id=repo_id,
                repo_type="model",
                operations=[CommitOperationAdd(path_in_repo="mine.txt", path_or_fileobj=b"mine\n")],
                commit_message="from a stale read",
                parent_commit=stale,
            )
        assert excinfo.value.response.status_code == 412
        # And it wrote nothing.
        assert "mine.txt" not in hf_api.list_repo_files(repo_id=repo_id, repo_type="model")
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model", missing_ok=True)


def test_create_commit_mixing_add_and_delete_applies_both(
    hf_api: HfApi, namespace: str, unique_name: str
) -> None:
    """A commit whose operations are partly applied must never answer 200.

    Both delete operations used to skip a malformed entry silently, which is
    the same shape of bug as the dropped copyFile: the caller is told the
    deletion happened.
    """
    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", exist_ok=True)
    try:
        hf_api.upload_file(
            path_or_fileobj=b"doomed\n",
            path_in_repo="doomed.txt",
            repo_id=repo_id,
            repo_type="model",
        )
        hf_api.create_commit(
            repo_id=repo_id,
            repo_type="model",
            operations=[
                CommitOperationAdd(path_in_repo="kept.txt", path_or_fileobj=b"kept\n"),
                CommitOperationDelete(path_in_repo="doomed.txt"),
            ],
            commit_message="add one, delete one",
        )
        files = hf_api.list_repo_files(repo_id=repo_id, repo_type="model")
        assert "kept.txt" in files
        assert "doomed.txt" not in files, "the delete half of the commit was dropped"
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model", missing_ok=True)


def test_list_repo_refs_carries_pull_requests(
    hf_api: HfApi, namespace: str, unique_name: str
) -> None:
    """include_pull_requests=True indexes data["pullRequests"] client-side, so
    a missing key is a KeyError rather than an empty list."""
    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", exist_ok=True)
    try:
        refs = hf_api.list_repo_refs(repo_id=repo_id, repo_type="model", include_pull_requests=True)
        assert refs.pull_requests == []
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model", missing_ok=True)
