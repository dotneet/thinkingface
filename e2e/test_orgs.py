"""End-to-end tests for the organization feature
(docs/dev/organization-design.md), driven through:

- the UI-facing organization API (`/api/v1/orgs/...`, docs/dev/organization-design.md
  §7.1) via plain `requests` + `Authorization: Bearer <token>`, since that
  surface has no `huggingface_hub` client, and
- `huggingface_hub` itself for everything an organization is meant to make
  possible: `whoami()["orgs"]`, `create_repo("org/name")`, `upload_file` /
  `hf_hub_download` gated by role, and `list_organization_members`.

Requires a running server; see e2e/README.md. `hf_api` / `hf_endpoint` /
`hf_token` come from conftest.py and act as the seeded admin user (a site
admin, so it is implicitly "admin" in every organization without needing an
`org_members` row -- docs/dev/organization-design.md §3).

Second (and third) accounts are freshly signed up per test, following the
`_signup` / `_mint_write_token` / `other_user` pattern in
test_repo_transfer.py -- reproduced here rather than shared, since pytest
fixtures defined in one test module are not visible to another without
promoting them to conftest.py.
"""

from __future__ import annotations

import uuid
from typing import NamedTuple

import pytest
import requests
from huggingface_hub import HfApi, hf_hub_download
from huggingface_hub.utils import HfHubHTTPError


class OtherUser(NamedTuple):
    """A freshly signed-up account with its own write-scoped token, used to
    exercise membership roles from outside the admin's own session."""

    username: str
    token: str
    headers: dict[str, str]
    api: HfApi


def _signup(endpoint: str, username: str, password: str = "password123") -> requests.Session:
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


def _new_user(hf_endpoint: str, label: str) -> tuple[OtherUser, requests.Session, int]:
    """Signs up a new account and mints a write-scoped token for it. Returns
    the OtherUser plus the raw session/token id the caller must tear down
    (revoke the token, close the session) -- kept separate from OtherUser so
    that plain functions (not just fixtures) can create as many of these as a
    given test scenario needs."""
    username = f"e2e-org-{label}-{uuid.uuid4().hex[:8]}"
    session = _signup(hf_endpoint, username)
    token_id, token = _mint_write_token(session, hf_endpoint, f"e2e-org-{label}")
    other = OtherUser(
        username=username,
        token=token,
        headers={"Authorization": f"Bearer {token}"},
        api=HfApi(endpoint=hf_endpoint, token=token),
    )
    return other, session, token_id


def _teardown_user(session: requests.Session, endpoint: str, token_id: int) -> None:
    try:
        session.delete(f"{endpoint}/api/v1/tokens/{token_id}", timeout=10)
    except requests.RequestException:
        pass
    session.close()


# ------------------------------------------------------------- org UI API


def _create_org(endpoint: str, headers: dict[str, str], name: str, **fields) -> requests.Response:
    body = {"name": name, **fields}
    return requests.post(f"{endpoint}/api/v1/orgs", headers=headers, json=body, timeout=10)


def _get_org(endpoint: str, headers: dict[str, str], org: str) -> requests.Response:
    return requests.get(f"{endpoint}/api/v1/orgs/{org}", headers=headers, timeout=10)


def _delete_org(endpoint: str, headers: dict[str, str], org: str) -> requests.Response:
    return requests.delete(f"{endpoint}/api/v1/orgs/{org}", headers=headers, timeout=10)


def _add_member(
    endpoint: str, headers: dict[str, str], org: str, username: str, role: str | None = None
) -> requests.Response:
    body: dict[str, str] = {"username": username}
    if role is not None:
        body["role"] = role
    return requests.post(
        f"{endpoint}/api/v1/orgs/{org}/members", headers=headers, json=body, timeout=10
    )


def _update_member_role(
    endpoint: str, headers: dict[str, str], org: str, username: str, role: str
) -> requests.Response:
    return requests.patch(
        f"{endpoint}/api/v1/orgs/{org}/members/{username}",
        headers=headers,
        json={"role": role},
        timeout=10,
    )


def _remove_member(
    endpoint: str, headers: dict[str, str], org: str, username: str
) -> requests.Response:
    return requests.delete(
        f"{endpoint}/api/v1/orgs/{org}/members/{username}", headers=headers, timeout=10
    )


def _delete_repos_ignore(hf_api: HfApi, repo_ids: list[tuple[str, str]]) -> None:
    """Best-effort cleanup of every (repo_id, repo_type) pair, ignoring repos
    that are already gone. A member org must have zero repositories before it
    can be deleted (docs/dev/organization-design.md §5 "Deleting an organization"), so this
    always has to run before `_delete_org`."""
    for repo_id, repo_type in repo_ids:
        try:
            hf_api.delete_repo(repo_id=repo_id, repo_type=repo_type)
        except HfHubHTTPError:
            pass


# ------------------------------------------------------------------ fixtures


@pytest.fixture()
def org_name(unique_name: str) -> str:
    return f"org-{unique_name}"


# --------------------------------------------------------------------- tests


def test_org_create_shows_up_in_whoami(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, org_name: str
) -> None:
    """Creating an organization makes the creator its admin, and that shows
    up in `whoami()["orgs"]` -- the shape `hf auth whoami` reads
    (docs/dev/organization-design.md §7.2)."""
    admin_headers = {"Authorization": f"Bearer {hf_token}"}
    create_resp = _create_org(hf_endpoint, admin_headers, org_name)
    assert create_resp.status_code == 201, create_resp.text
    try:
        whoami = hf_api.whoami()
        orgs_by_name = {org["name"]: org for org in whoami.get("orgs", [])}
        assert org_name in orgs_by_name, whoami.get("orgs")
        assert orgs_by_name[org_name]["roleInOrg"] == "admin"

        # Also visible through the UI API itself, with the same role.
        get_resp = _get_org(hf_endpoint, admin_headers, org_name)
        assert get_resp.status_code == 200, get_resp.text
        assert get_resp.json()["org"]["viewer_role"] == "admin"
    finally:
        _delete_org(hf_endpoint, admin_headers, org_name)


def test_org_membership_roles_gate_repo_access(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, org_name: str, unique_name: str
) -> None:
    """The core role story: a `read` member can clone an org repo but not push
    to it, and promoting that member to `write` unlocks pushing.
    `list_organization_members` sees both of them.

    Repositories carry no visibility of their own, so reading is open to
    everyone; what the roles gate is writing."""
    admin_headers = {"Authorization": f"Bearer {hf_token}"}
    repo_id = f"{org_name}/{unique_name}"
    member, member_session, member_token_id = _new_user(hf_endpoint, "member")
    repos: list[tuple[str, str]] = []
    try:
        assert _create_org(hf_endpoint, admin_headers, org_name).status_code == 201

        # --- create the repo under the org ---------------------------------
        hf_api.create_repo(repo_id=repo_id, repo_type="model")
        repos.append((repo_id, "model"))
        info = hf_api.repo_info(repo_id=repo_id, repo_type="model")
        assert info.id == repo_id

        content = b"org membership e2e\n"
        hf_api.upload_file(
            path_or_fileobj=content,
            path_in_repo="README.md",
            repo_id=repo_id,
            repo_type="model",
            commit_message="Add README",
        )

        # --- add member as read: can download, cannot push ------------------
        add_resp = _add_member(hf_endpoint, admin_headers, org_name, member.username, "read")
        assert add_resp.status_code == 201, add_resp.text
        assert add_resp.json()["member"]["role"] == "read"

        downloaded = hf_hub_download(
            repo_id=repo_id, repo_type="model", filename="README.md", token=member.token
        )
        with open(downloaded, "rb") as f:
            assert f.read() == content

        with pytest.raises(HfHubHTTPError) as excinfo:
            member.api.upload_file(
                path_or_fileobj=b"should not land\n",
                path_in_repo="blocked.txt",
                repo_id=repo_id,
                repo_type="model",
                commit_message="read member tries to push",
            )
        assert excinfo.value.response.status_code == 403

        # --- promote to write: push now succeeds -----------------------------
        upgrade_resp = _update_member_role(
            hf_endpoint, admin_headers, org_name, member.username, "write"
        )
        assert upgrade_resp.status_code == 200, upgrade_resp.text
        assert upgrade_resp.json()["member"]["role"] == "write"

        member.api.upload_file(
            path_or_fileobj=b"written by a write-role member\n",
            path_in_repo="allowed.txt",
            repo_id=repo_id,
            repo_type="model",
            commit_message="write member pushes",
        )
        files = hf_api.list_repo_files(repo_id=repo_id, repo_type="model")
        assert "allowed.txt" in files

        # --- list_organization_members returns both -------------------------
        members = {m.username: m for m in hf_api.list_organization_members(org_name)}
        assert set(members) == {hf_api.whoami()["name"], member.username}
    finally:
        _delete_repos_ignore(hf_api, repos)
        _delete_org(hf_endpoint, admin_headers, org_name)
        _teardown_user(member_session, hf_endpoint, member_token_id)


def test_org_last_admin_is_protected(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, org_name: str
) -> None:
    """An organization always needs at least one admin: demoting or removing
    the last one is rejected with 409 (docs/dev/organization-design.md §5)."""
    admin_headers = {"Authorization": f"Bearer {hf_token}"}
    creator_username = hf_api.whoami()["name"]
    try:
        assert _create_org(hf_endpoint, admin_headers, org_name).status_code == 201

        demote_resp = _update_member_role(
            hf_endpoint, admin_headers, org_name, creator_username, "write"
        )
        assert demote_resp.status_code == 409, demote_resp.text
        assert demote_resp.json()["error"]["type"] == "last_admin"

        remove_resp = _remove_member(hf_endpoint, admin_headers, org_name, creator_username)
        assert remove_resp.status_code == 409, remove_resp.text
        assert remove_resp.json()["error"]["type"] == "last_admin"

        # The org is unaffected: creator is still admin.
        get_resp = _get_org(hf_endpoint, admin_headers, org_name)
        assert get_resp.json()["org"]["viewer_role"] == "admin"
    finally:
        _delete_org(hf_endpoint, admin_headers, org_name)


def test_org_delete_requires_no_repositories(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, org_name: str, unique_name: str
) -> None:
    """An organization with repositories cannot be deleted (409); once they
    are gone, deletion succeeds (204) (docs/dev/organization-design.md §5)."""
    admin_headers = {"Authorization": f"Bearer {hf_token}"}
    repo_id = f"{org_name}/{unique_name}"
    repos: list[tuple[str, str]] = []
    deleted_ok = False
    try:
        assert _create_org(hf_endpoint, admin_headers, org_name).status_code == 201
        hf_api.create_repo(repo_id=repo_id, repo_type="model")
        repos.append((repo_id, "model"))

        blocked_resp = _delete_org(hf_endpoint, admin_headers, org_name)
        assert blocked_resp.status_code == 409, blocked_resp.text
        assert blocked_resp.json()["error"]["type"] == "has_repositories"

        _delete_repos_ignore(hf_api, repos)
        repos.clear()

        final_resp = _delete_org(hf_endpoint, admin_headers, org_name)
        assert final_resp.status_code == 204, final_resp.text
        deleted_ok = True

        # A second delete has nothing left to act on.
        after_resp = _get_org(hf_endpoint, admin_headers, org_name)
        assert after_resp.status_code == 404
    finally:
        _delete_repos_ignore(hf_api, repos)
        if not deleted_ok:
            _delete_org(hf_endpoint, admin_headers, org_name)
