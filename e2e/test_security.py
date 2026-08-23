"""End-to-end checks for the hardening in todo/security-audit-findings.md.

These live next to the compatibility suite on purpose: every fix in that audit
touched an endpoint `huggingface_hub` or `git` also uses, so the question each
test answers is "did the hardening hold, *and* did the client keep working?".

Requires a running server; see e2e/README.md.
"""

from __future__ import annotations

import io
import uuid

import pyarrow as pa
import pyarrow.parquet as pq
import pytest
import requests
from huggingface_hub import HfApi, hf_hub_download


def test_resolve_serves_downloads_as_attachments_and_hf_download_still_works(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, namespace: str, unique_name: str
) -> None:
    """[S1] A pushed .html must not be renderable on the API origin.

    The important half of this test is the second one: `hf_hub_download` must
    keep working with `Content-Disposition: attachment` on the response, since
    that header is what neutralises the stored XSS.
    """
    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    try:
        hf_api.upload_file(
            path_or_fileobj=b"<script>alert(document.domain)</script>",
            path_in_repo="poc.html",
            repo_id=repo_id,
            commit_message="Add an html file",
        )
        hf_api.upload_file(
            path_or_fileobj=b'{"hidden_size": 8}',
            path_in_repo="config.json",
            repo_id=repo_id,
            commit_message="Add config",
        )

        resp = requests.get(
            f"{hf_endpoint}/{repo_id}/resolve/main/poc.html",
            headers={"Authorization": f"Bearer {hf_token}"},
            timeout=30,
        )
        resp.raise_for_status()
        assert resp.headers["X-Content-Type-Options"] == "nosniff"
        assert resp.headers["Content-Disposition"].startswith("attachment;")
        # text/html would execute in the API origin, which holds tf_session.
        assert "text/html" not in resp.headers["Content-Type"]

        # The download path huggingface_hub actually uses is unaffected.
        local = hf_hub_download(repo_id=repo_id, filename="config.json")
        with open(local, encoding="utf-8") as f:
            assert f.read() == '{"hidden_size": 8}'
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model")


def test_lfs_batch_download_refuses_an_oid_the_repo_does_not_own(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, namespace: str
) -> None:
    """[S3] Knowing an oid must not be enough to pull the bytes.

    Uploads a parquet into one repository (which puts its sha256 in the
    bucket), then asks a *second* repository's LFS batch endpoint for the same
    oid. The second repository never saw the object, so the answer must be a
    per-object 404 -- and the first repository must still be able to fetch it.
    """
    source_id = f"{namespace}/e2e-{uuid.uuid4().hex[:10]}"
    other_id = f"{namespace}/e2e-{uuid.uuid4().hex[:10]}"
    hf_api.create_repo(repo_id=source_id, repo_type="dataset", private=True)
    hf_api.create_repo(repo_id=other_id, repo_type="dataset", private=False)
    try:
        table = pa.table({"id": pa.array(range(16))})
        buf = io.BytesIO()
        pq.write_table(table, buf)
        buf.seek(0)
        hf_api.upload_file(
            path_or_fileobj=buf,
            path_in_repo="data/train.parquet",
            repo_id=source_id,
            repo_type="dataset",
            commit_message="Add parquet",
        )

        # Learn the oid the way an attacker would: the tree lists it.
        files = hf_api.list_repo_tree(
            repo_id=source_id, repo_type="dataset", path_in_repo="data", recursive=True
        )
        oid = None
        size = None
        for entry in files:
            lfs = getattr(entry, "lfs", None)
            if lfs is not None:
                oid = lfs.sha256
                size = lfs.size
                break
        assert oid is not None, "the parquet was not stored as an LFS object"

        def batch(repo_id: str) -> dict:
            resp = requests.post(
                f"{hf_endpoint}/datasets/{repo_id}/info/lfs/objects/batch",
                json={"operation": "download", "objects": [{"oid": oid, "size": size}]},
                headers={
                    "Authorization": f"Bearer {hf_token}",
                    "Content-Type": "application/vnd.git-lfs+json",
                    "Accept": "application/vnd.git-lfs+json",
                },
                timeout=30,
            )
            resp.raise_for_status()
            return resp.json()

        # Owner: still gets a download action.
        own = batch(source_id)["objects"][0]
        assert own.get("error") is None, own
        assert "download" in own.get("actions", {}), own

        # A repository that does not own the oid: per-object 404, no actions.
        # Note this is the *same* admin account, so the read permission is
        # satisfied -- only the ownership check can stop it.
        foreign = batch(other_id)["objects"][0]
        assert foreign.get("actions") in (None, {}), foreign
        assert foreign.get("error", {}).get("code") == 404, foreign
    finally:
        hf_api.delete_repo(repo_id=source_id, repo_type="dataset")
        hf_api.delete_repo(repo_id=other_id, repo_type="dataset")


def test_validate_yaml_requires_authentication_but_create_commit_still_works(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, namespace: str, unique_name: str
) -> None:
    """[S19] /api/validate-yaml is no longer an anonymous YAML parser.

    huggingface_hub only ever calls it from create_commit with the commit's
    own token, so requiring one costs nothing -- which the README upload at
    the end of this test is what proves.
    """
    anon = requests.post(
        f"{hf_endpoint}/api/validate-yaml",
        json={"content": "---\ntags: [x]\n---\n", "repoType": "model"},
        timeout=30,
    )
    assert anon.status_code == 401, anon.text

    authed = requests.post(
        f"{hf_endpoint}/api/validate-yaml",
        json={"content": "---\ntags: [x]\n---\n", "repoType": "model"},
        headers={"Authorization": f"Bearer {hf_token}"},
        timeout=30,
    )
    assert authed.status_code == 200, authed.text

    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    try:
        # A README with front matter is exactly what triggers _validate_yaml.
        hf_api.upload_file(
            path_or_fileobj=b"---\nlicense: apache-2.0\n---\n\n# card\n",
            path_in_repo="README.md",
            repo_id=repo_id,
            commit_message="Add card",
        )
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model")


def test_login_rate_limit_does_not_fire_on_successful_logins() -> None:
    """[S2] Only failures are counted.

    The e2e suite itself logs in from one address, so a limiter that counted
    successes would break this whole file. Also checks that a run of wrong
    passwords does eventually get a 429.
    """
    from conftest import TF_ADMIN_PASSWORD, TF_ADMIN_USERNAME, TF_ENDPOINT

    for _ in range(5):
        ok = requests.post(
            f"{TF_ENDPOINT}/api/v1/auth/login",
            json={"username": TF_ADMIN_USERNAME, "password": TF_ADMIN_PASSWORD},
            timeout=30,
        )
        assert ok.status_code == 200, ok.text

    # A username that does not exist, so nothing real can be locked out by
    # this test. The per-username bucket is the tighter of the two.
    victim = f"nobody-{uuid.uuid4().hex[:8]}"
    statuses = []
    for _ in range(12):
        resp = requests.post(
            f"{TF_ENDPOINT}/api/v1/auth/login",
            json={"username": victim, "password": "wrong-password"},
            timeout=30,
        )
        statuses.append(resp.status_code)
        if resp.status_code == 429:
            assert resp.headers.get("Retry-After")
            assert resp.json()["error"]["type"] == "rate_limited"
            break
    else:
        pytest.fail(f"12 wrong passwords never produced a 429: {statuses}")

    # The real account is untouched by the other username's bucket.
    still_ok = requests.post(
        f"{TF_ENDPOINT}/api/v1/auth/login",
        json={"username": TF_ADMIN_USERNAME, "password": TF_ADMIN_PASSWORD},
        timeout=30,
    )
    assert still_ok.status_code == 200, still_ok.text


def test_security_headers_and_cors_allowlist(hf_endpoint: str) -> None:
    """[S4] / [S10] Headers that must be on every response."""
    resp = requests.get(f"{hf_endpoint}/healthz", timeout=30)
    assert resp.headers["X-Content-Type-Options"] == "nosniff"
    assert resp.headers["X-Frame-Options"] == "DENY"
    assert resp.headers["Referrer-Policy"] == "strict-origin-when-cross-origin"

    hostile = requests.options(
        f"{hf_endpoint}/api/v1/me",
        headers={"Origin": "https://evil.example", "Access-Control-Request-Method": "GET"},
        timeout=30,
    )
    assert "Access-Control-Allow-Origin" not in hostile.headers, hostile.headers
    assert "Access-Control-Allow-Credentials" not in hostile.headers, hostile.headers
