"""Shared fixtures for the thinkingface <-> huggingface_hub compatibility suite.

Requires a running thinkingface server (`docker compose up -d` from the repo
root, or `make up`). This module logs in as the seeded admin user, mints a
write-scoped API token, and points `HF_ENDPOINT` / `HF_TOKEN` at the server
*before* `huggingface_hub` is imported anywhere in this process.

That ordering matters: `huggingface_hub.constants.ENDPOINT` (and the
default `HfApi` instance the top-level convenience functions are bound to)
are resolved once, at `huggingface_hub` import time, from the `HF_ENDPOINT`
env var. Setting it inside a fixture function would be too late, since
pytest has already imported the test modules (and therefore
`huggingface_hub`) by the time any fixture runs. Hence this happens at
conftest module load, ahead of the `from huggingface_hub import ...` below.
"""

from __future__ import annotations

import os
import socket
import urllib.parse
import uuid

import pytest
import requests

# thinkingface moves large files over Git LFS, not Xet. huggingface_hub >= 1.0
# prefers Xet whenever the hf_xet package happens to be installed, so turn it
# off here before huggingface_hub is imported.
os.environ.setdefault("HF_HUB_DISABLE_XET", "1")

TF_ENDPOINT = os.environ.get("TF_ENDPOINT", "http://localhost:8080").rstrip("/")
TF_ADMIN_USERNAME = os.environ.get("TF_ADMIN_USERNAME", "admin")
TF_ADMIN_PASSWORD = os.environ.get("TF_ADMIN_PASSWORD", "admin")

# git over SSH (TF_SSH_ENABLED / TF_SSH_ADDR, docs/dev/thinkingface-design.md
# §5). Resolved the same way as TF_ENDPOINT -- an env var with a default that
# matches docker-compose.yml -- but unlike the HTTP endpoint it is *optional*:
# the listener can be switched off, and it is published on its own port that a
# reverse proxy or a hosted deployment may not expose at all. The ssh_endpoint
# fixture below therefore skips rather than fails when nothing answers, so a
# suite run against such a deployment stays green on everything else.
TF_SSH_HOST = os.environ.get(
    "TF_SSH_HOST", urllib.parse.urlsplit(TF_ENDPOINT).hostname or "localhost"
)
TF_SSH_PORT = int(os.environ.get("TF_SSH_PORT", "2222"))

_UNREACHABLE_HELP = (
    f"Could not reach a thinkingface server at {TF_ENDPOINT}. "
    "Start the stack first, e.g. `docker compose up -d` or `make up` "
    "from the repository root, then re-run pytest. "
    "Override the target with the TF_ENDPOINT env var."
)
_LOGIN_HELP = (
    f"A thinkingface server answered at {TF_ENDPOINT}, but logging in as "
    f"{TF_ADMIN_USERNAME!r} was rejected ({{status}}). Set TF_ADMIN_USERNAME / "
    "TF_ADMIN_PASSWORD to match the server's seeded admin account (see .env)."
)

# Set by _bootstrap_hf_token so the session finalizer can revoke the token it
# minted instead of leaving a write-scoped credential behind on every run.
_bootstrap_session: requests.Session | None = None
_bootstrap_token_id: int | None = None


def _bootstrap_hf_token() -> str:
    """Log in as the seeded admin user and mint a write-scoped API token.

    Uses `requests` directly (not huggingface_hub) since this runs before
    HF_ENDPOINT/HF_TOKEN are set in the environment.

    Failures are re-raised as a RuntimeError carrying an actionable message and
    never the credentials themselves: the password only ever appears in the
    request body, so it stays out of the traceback even under `pytest -l`.
    """
    global _bootstrap_session, _bootstrap_token_id

    session = requests.Session()
    try:
        login = session.post(
            f"{TF_ENDPOINT}/api/v1/auth/login",
            json={"username": TF_ADMIN_USERNAME, "password": TF_ADMIN_PASSWORD},
            timeout=10,
        )
    except requests.RequestException as exc:
        # Connection/DNS/timeout: the server really is unreachable. Drop the
        # cause so the request object (which holds the password in its body)
        # is not attached to the reported exception.
        raise RuntimeError(f"{_UNREACHABLE_HELP} ({type(exc).__name__})") from None
    if login.status_code >= 400:
        raise RuntimeError(_LOGIN_HELP.format(status=login.status_code)) from None

    try:
        token_resp = session.post(
            f"{TF_ENDPOINT}/api/v1/tokens",
            json={"name": f"e2e-{uuid.uuid4().hex[:8]}", "scope": "write"},
            timeout=10,
        )
        token_resp.raise_for_status()
        body = token_resp.json()
    except requests.RequestException as exc:
        raise RuntimeError(f"Could not mint an e2e API token: {exc}") from None
    except ValueError as exc:  # non-JSON body
        raise RuntimeError(f"POST /api/v1/tokens did not return JSON: {exc}") from None

    if "token" not in body:
        raise RuntimeError(
            "POST /api/v1/tokens returned no 'token' field; "
            f"keys were {sorted(body)!r} (has the API contract changed?)"
        )
    _bootstrap_session = session
    _bootstrap_token_id = body.get("id")
    return body["token"]


def pytest_sessionfinish(session, exitstatus) -> None:  # noqa: ARG001
    """Revoke the write-scoped token minted at import time.

    Without this every `pytest` run leaves a live write credential in the
    server's token table. Best-effort: a failure here must never turn a green
    run red, and the token value is never logged.
    """
    if _bootstrap_session is None or _bootstrap_token_id is None:
        return
    try:
        _bootstrap_session.delete(f"{TF_ENDPOINT}/api/v1/tokens/{_bootstrap_token_id}", timeout=10)
    except requests.RequestException:
        pass
    finally:
        _bootstrap_session.close()


# --- module-level bootstrap, must happen before `import huggingface_hub` ---
os.environ["HF_ENDPOINT"] = TF_ENDPOINT
os.environ["HF_TOKEN"] = _bootstrap_hf_token()

from huggingface_hub import HfApi  # noqa: E402  (see module docstring)


@pytest.fixture(scope="session")
def hf_endpoint() -> str:
    return TF_ENDPOINT


@pytest.fixture(scope="session")
def hf_token() -> str:
    return os.environ["HF_TOKEN"]


@pytest.fixture(scope="session")
def hf_api(hf_endpoint: str, hf_token: str) -> HfApi:
    return HfApi(endpoint=hf_endpoint, token=hf_token)


@pytest.fixture(scope="session")
def namespace(hf_api: HfApi) -> str:
    """The admin user's namespace, i.e. where created repos live."""
    return hf_api.whoami()["name"]


@pytest.fixture()
def unique_name() -> str:
    return f"e2e-{uuid.uuid4().hex[:10]}"


@pytest.fixture(scope="session")
def ssh_endpoint(hf_api: HfApi, namespace: str) -> tuple[str, int]:
    """(host, port) of *this instance's* git-over-SSH listener.

    git over SSH is optional (TF_SSH_ENABLED) and lives on a separate port, so
    "not offered" is a valid deployment rather than a compatibility
    regression, and the fixture skips instead of failing.

    The address comes from the server itself -- `ssh_clone_url` on any
    repository, empty when SSH is off -- rather than from a hardcoded default.
    A TCP probe alone is not enough: a developer running the suite against a
    second API on another port (`make dev-api` on :8081) still has the compose
    stack's SSH listener answering on :2222, and a probe would happily accept
    it. The keys these tests register live in the API's database, so pointing
    at a *different* instance's SSH server turns every case into
    "Permission denied (publickey)" -- a failure that looks exactly like a
    real regression. Asking the API which address is its own removes the
    ambiguity entirely.

    TF_SSH_HOST / TF_SSH_PORT still override, for a deployment reached through
    a tunnel or a port-forward where the advertised address is not dialable.
    """
    probe = f"{namespace}/ssh-probe-{uuid.uuid4().hex[:8]}"
    hf_api.create_repo(repo_id=probe, repo_type="model", exist_ok=True)
    try:
        # Reads need no credentials on this server, so a plain GET is enough.
        resp = requests.get(f"{TF_ENDPOINT}/api/v1/repos/model/{probe}", timeout=10)
        resp.raise_for_status()
        advertised = resp.json().get("repo", {}).get("ssh_clone_url", "")
    finally:
        hf_api.delete_repo(repo_id=probe, repo_type="model", missing_ok=True)

    # Either override is enough to mean "I know where it is, test it anyway".
    # Checking only one of them made the documented escape hatch a no-op for
    # anybody who used the other, and then answered with the very instruction
    # they had just followed.
    overridden = "TF_SSH_HOST" in os.environ or "TF_SSH_PORT" in os.environ
    if not advertised and not overridden:
        pytest.skip(
            f"the server at {TF_ENDPOINT} advertises no ssh_clone_url, so git "
            "over SSH is disabled there (TF_SSH_ENABLED). Set TF_SSH_HOST "
            "and/or TF_SSH_PORT to test a listener it does not advertise."
        )

    split = urllib.parse.urlsplit(advertised) if advertised else None
    host = os.environ.get("TF_SSH_HOST") or (split.hostname if split else None) or TF_SSH_HOST
    port = int(os.environ.get("TF_SSH_PORT", 0)) or (split.port if split else None) or TF_SSH_PORT

    try:
        with socket.create_connection((host, port), timeout=5):
            pass
    except OSError as exc:
        pytest.skip(
            f"the server advertises git over SSH at {host}:{port} but nothing "
            f"answers there ({type(exc).__name__}) -- typical when the port is "
            "not published to the host. Override with TF_SSH_HOST / TF_SSH_PORT."
        )
    return host, port
