"""End-to-end tests for the namespace feature (docs/dev/namespace-design.md),
which unifies "username" and "organization ID" into one concept -- a
namespace -- exposed through `/{ns}` on the web UI and, for tests, through:

- the UI-facing namespace API (`GET /api/v1/namespaces/{ns}`,
  `PATCH /api/v1/me/profile`, `GET /api/v1/experiments?author=`,
  docs/dev/namespace-design.md §7.1) via plain `requests` +
  `Authorization: Bearer <token>` -- that surface has no `huggingface_hub`
  client, same as `test_orgs.py`'s `/api/v1/orgs/...` coverage, and
- `huggingface_hub` for the HF-compatible surface a namespace's profile is
  meant to feed: `whoami()["fullname"]`, `get_user_overview()`,
  `get_organization_overview()` (docs/dev/namespace-design.md §7.2).

Requires a running server; see e2e/README.md. `hf_api` / `hf_endpoint` /
`hf_token` come from conftest.py and act as the seeded admin user. Second
accounts and organizations are created fresh per test, reusing the
`_signup` / `_mint_token` / `OtherUser` pattern from `test_orgs.py` and
`test_repo_transfer.py` (reproduced here rather than shared -- pytest
fixtures defined in one test module are not visible to another without
promoting them to conftest.py, which is a deliberate non-goal, see those
modules' docstrings).
"""

from __future__ import annotations

import uuid
from typing import NamedTuple

import pytest
import requests
from huggingface_hub import HfApi
from huggingface_hub.utils import HfHubHTTPError


class OtherUser(NamedTuple):
    """A freshly signed-up account with its own token, used to exercise
    `whoami()` / profile endpoints from outside the admin's own session."""

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


def _mint_token(
    session: requests.Session, endpoint: str, name: str, scope: str = "write"
) -> tuple[int, str]:
    resp = session.post(
        f"{endpoint}/api/v1/tokens", json={"name": name, "scope": scope}, timeout=10
    )
    resp.raise_for_status()
    body = resp.json()
    return body["id"], body["token"]


def _new_user(
    hf_endpoint: str, label: str, scope: str = "write"
) -> tuple[OtherUser, requests.Session, int]:
    """Signs up a new account and mints a token for it (write-scoped by
    default). Returns the OtherUser plus the raw session/token id the caller
    must tear down (revoke the token, close the session)."""
    username = f"e2e-ns-{label}-{uuid.uuid4().hex[:8]}"
    session = _signup(hf_endpoint, username)
    token_id, token = _mint_token(session, hf_endpoint, f"e2e-ns-{label}", scope=scope)
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


# ------------------------------------------------------------ namespace API


def _get_namespace(
    endpoint: str, ns: str, headers: dict[str, str] | None = None
) -> requests.Response:
    return requests.get(f"{endpoint}/api/v1/namespaces/{ns}", headers=headers, timeout=10)


def _patch_profile(endpoint: str, headers: dict[str, str], **fields) -> requests.Response:
    return requests.patch(f"{endpoint}/api/v1/me/profile", headers=headers, json=fields, timeout=10)


def _list_experiments(endpoint: str, headers: dict[str, str], **params) -> requests.Response:
    return requests.get(
        f"{endpoint}/api/v1/experiments", headers=headers, params=params, timeout=10
    )


def _create_org(endpoint: str, headers: dict[str, str], name: str, **fields) -> requests.Response:
    body = {"name": name, **fields}
    return requests.post(f"{endpoint}/api/v1/orgs", headers=headers, json=body, timeout=10)


def _delete_org(endpoint: str, headers: dict[str, str], org: str) -> requests.Response:
    return requests.delete(f"{endpoint}/api/v1/orgs/{org}", headers=headers, timeout=10)


# --------------------------------------------------------------------- tests


def test_fresh_signup_has_an_empty_public_namespace(hf_endpoint: str) -> None:
    """A user who just signed up and never pushed anything still has a
    namespace: `GET /api/v1/namespaces/{u}` is 200 with every count at 0
    (docs/dev/namespace-design.md §5.5, "an empty namespace should not 404"),
    reachable anonymously, and `can_edit` is true only for the account
    itself."""
    user, session, token_id = _new_user(hf_endpoint, "fresh")
    try:
        anon_resp = _get_namespace(hf_endpoint, user.username)
        assert anon_resp.status_code == 200, anon_resp.text
        anon_ns = anon_resp.json()["namespace"]
        assert anon_ns["name"] == user.username
        assert anon_ns["kind"] == "user"
        assert anon_ns["num_models"] == 0
        assert anon_ns["num_datasets"] == 0
        assert anon_ns["num_experiments"] == 0
        assert anon_ns["num_members"] == 0
        assert anon_ns["can_edit"] is False

        own_resp = _get_namespace(hf_endpoint, user.username, headers=user.headers)
        assert own_resp.status_code == 200, own_resp.text
        assert own_resp.json()["namespace"]["can_edit"] is True
    finally:
        _teardown_user(session, hf_endpoint, token_id)


def test_namespace_lookup_is_case_insensitive_but_returns_canonical_spelling(
    hf_endpoint: str,
) -> None:
    """Namespace names are matched case-insensitively but the response
    always carries the spelling used at registration (docs/dev/namespace-design.md
    §5.5), e.g. looking up `Alice` for a user registered as `alice` returns
    `name: "alice"`."""
    user, session, token_id = _new_user(hf_endpoint, "case")
    try:
        mixed_case = user.username.upper()
        resp = _get_namespace(hf_endpoint, mixed_case)
        assert resp.status_code == 200, resp.text
        assert resp.json()["namespace"]["name"] == user.username
    finally:
        _teardown_user(session, hf_endpoint, token_id)


def test_namespace_num_models_counts_created_repos(hf_endpoint: str, unique_name: str) -> None:
    """Creating a model repository under a namespace is reflected in that
    namespace's `num_models` (docs/dev/namespace-design.md §6, `CountNamespaceResources`)."""
    user, session, token_id = _new_user(hf_endpoint, "count")
    repo_id = f"{user.username}/{unique_name}"
    created = False
    try:
        user.api.create_repo(repo_id=repo_id, repo_type="model")
        created = True

        resp = _get_namespace(hf_endpoint, user.username)
        assert resp.status_code == 200, resp.text
        assert resp.json()["namespace"]["num_models"] == 1
    finally:
        if created:
            try:
                user.api.delete_repo(repo_id=repo_id, repo_type="model")
            except HfHubHTTPError:
                pass
        _teardown_user(session, hf_endpoint, token_id)


def test_profile_update_reflects_in_whoami_and_user_overview(hf_endpoint: str) -> None:
    """`PATCH /api/v1/me/profile` is a partial update; its fields show up in
    `whoami()["fullname"]` (`display_name || username`) and
    `get_user_overview().fullname` / `.details` (docs/dev/namespace-design.md
    §5.3, §7.2). A `javascript:` URL for `website` is rejected with 400
    (docs/dev/namespace-design.md §10), and a read-scoped token cannot call the
    endpoint at all (403)."""
    user, session, token_id = _new_user(hf_endpoint, "profile")
    try:
        display_name = f"Display {user.username}"
        description = "hello from e2e"
        website = "https://example.com/" + user.username

        patch_resp = _patch_profile(
            hf_endpoint,
            user.headers,
            display_name=display_name,
            description=description,
            website=website,
        )
        assert patch_resp.status_code == 200, patch_resp.text
        patched_ns = patch_resp.json()["namespace"]
        assert patched_ns["display_name"] == display_name
        assert patched_ns["description"] == description
        assert patched_ns["website"] == website

        whoami = user.api.whoami()
        assert whoami["fullname"] == display_name

        overview = user.api.get_user_overview(user.username)
        assert overview.fullname == display_name
        assert overview.details == description

        bad_resp = _patch_profile(hf_endpoint, user.headers, website="javascript:alert(1)")
        assert bad_resp.status_code == 400, bad_resp.text

        read_token_id, read_token = _mint_token(
            session, hf_endpoint, "e2e-ns-profile-read", scope="read"
        )
        try:
            read_headers = {"Authorization": f"Bearer {read_token}"}
            forbidden_resp = _patch_profile(hf_endpoint, read_headers, display_name="nope")
            assert forbidden_resp.status_code == 403, forbidden_resp.text
        finally:
            try:
                session.delete(f"{hf_endpoint}/api/v1/tokens/{read_token_id}", timeout=10)
            except requests.RequestException:
                pass
    finally:
        _teardown_user(session, hf_endpoint, token_id)


def test_organization_namespace_and_overview(
    hf_endpoint: str, hf_api: HfApi, hf_token: str
) -> None:
    """An organization is a namespace with `kind == "org"`; its member count
    shows up both in `GET /api/v1/namespaces/{org}` and in
    `get_organization_overview().num_users`. Passing a *user* namespace to
    `get_organization_overview()` (or an org namespace to `get_user_overview()`)
    404s, same as upstream HF (docs/dev/namespace-design.md §7.2)."""
    admin_headers = {"Authorization": f"Bearer {hf_token}"}
    org_name = f"e2e-ns-org-{uuid.uuid4().hex[:8]}"
    try:
        create_resp = _create_org(hf_endpoint, admin_headers, org_name)
        assert create_resp.status_code == 201, create_resp.text

        overview = hf_api.get_organization_overview(org_name)
        assert overview.num_users == 1

        ns_resp = _get_namespace(hf_endpoint, org_name)
        assert ns_resp.status_code == 200, ns_resp.text
        ns = ns_resp.json()["namespace"]
        assert ns["kind"] == "org"
        assert ns["num_members"] == 1

        with pytest.raises(HfHubHTTPError) as excinfo:
            hf_api.get_user_overview(org_name)
        assert excinfo.value.response.status_code == 404
    finally:
        _delete_org(hf_endpoint, admin_headers, org_name)


def test_reserved_namespace_name_is_404(hf_endpoint: str) -> None:
    """A reserved name (a static top-level route, here `models`) can never be
    registered, so `GET /api/v1/namespaces/models` is a plain 404 -- the
    lookup only checks the name's syntax and then finds nobody holds it
    (docs/dev/namespace-design.md §5.5, §9)."""
    resp = _get_namespace(hf_endpoint, "models")
    assert resp.status_code == 404, resp.text


def test_nonexistent_namespace_is_404(hf_endpoint: str) -> None:
    """A syntactically valid name nobody has registered is 404, not an
    empty-but-existing namespace."""
    resp = _get_namespace(hf_endpoint, f"e2e-ns-nobody-{uuid.uuid4().hex[:10]}")
    assert resp.status_code == 404, resp.text


def test_experiments_author_filter_returns_total(
    hf_endpoint: str, hf_token: str, namespace: str
) -> None:
    """`GET /api/v1/experiments?author=` filters by namespace (case-insensitive)
    and the response carries `total` (docs/dev/namespace-design.md §5.6). A fresh
    namespace's experiment tab is not expected to have any experiment
    repositories from other tests, so this only asserts the shape and that
    `total` is a non-negative count -- not an exact value, since it isn't this
    test's job to guarantee experiment repos exist for the admin namespace."""
    admin_headers = {"Authorization": f"Bearer {hf_token}"}
    resp = _list_experiments(hf_endpoint, admin_headers, author=namespace)
    assert resp.status_code == 200, resp.text
    body = resp.json()
    assert "total" in body, body
    assert isinstance(body["total"], int)
    assert body["total"] >= 0
    # `total` counts every match; `items` is one page (default limit 100).
    assert len(body["items"]) <= body["total"]

    # Case-insensitive: the same query upper-cased returns the same total.
    resp_upper = _list_experiments(hf_endpoint, admin_headers, author=namespace.upper())
    assert resp_upper.status_code == 200, resp_upper.text
    assert resp_upper.json()["total"] == body["total"]

    # An author nobody owns returns zero, not an error.
    resp_nobody = _list_experiments(
        hf_endpoint, admin_headers, author=f"e2e-ns-nobody-{uuid.uuid4().hex[:10]}"
    )
    assert resp_nobody.status_code == 200, resp_nobody.text
    assert resp_nobody.json()["total"] == 0
