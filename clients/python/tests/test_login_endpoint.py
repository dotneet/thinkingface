"""Regression tests for ``thinkingface.login()``'s HF_ENDPOINT-after-import bug.

``huggingface_hub`` resolves its default endpoint exactly once: when its
``constants`` submodule is first imported, into ``constants.ENDPOINT``, and its
module-level ``HfApi`` singleton (``huggingface_hub.hf_api.api`` -- the object
backing the top-level ``login`` / ``whoami`` / ``create_repo`` / ``upload_file``
/ ... functions) captures that value into its own ``endpoint`` attribute at
construction and never re-reads it. Calling ``thinkingface.login(endpoint,
token=...)`` *after* something already imported ``huggingface_hub`` used to set
``os.environ["HF_ENDPOINT"]`` and then call ``huggingface_hub.login(token=...)``
regardless -- which sends ``token`` to whoami on whatever endpoint those
already-built internals still hold (huggingface.co by default), leaking the
thinkingface token to the real Hub.

``thinkingface.login()`` now retargets those already-built internals
best-effort (``_retarget_imported_hf_hub``), and refuses to call
``huggingface_hub.login()`` at all -- raising instead -- unless it can verify
the retarget actually took effect. These tests never touch the network:
``huggingface_hub.login`` is replaced with a stand-in, and the "already
imported" internals are simulated with fake stub modules injected into
``sys.modules`` under ``huggingface_hub.constants`` / ``huggingface_hub.hf_api``
so no test depends on the installed huggingface_hub version's private layout.
"""

from __future__ import annotations

import os
import sys
import types
from typing import Any
from unittest.mock import Mock

import pytest

import thinkingface


@pytest.fixture(autouse=True)
def _clean_env(monkeypatch: pytest.MonkeyPatch) -> None:
    for name in ("HF_ENDPOINT", "HF_TOKEN", "HF_HUB_DISABLE_XET"):
        monkeypatch.delenv(name, raising=False)
    # login() retargets every loaded huggingface_hub module holding a copy of
    # ENDPOINT, including the real ones this process imported; put them back
    # afterwards so no test leaks a localhost endpoint into the next.
    for name, mod in list(sys.modules.items()):
        if (name == "huggingface_hub" or name.startswith("huggingface_hub.")) and isinstance(
            getattr(mod, "ENDPOINT", None), str
        ):
            monkeypatch.setattr(mod, "ENDPOINT", mod.ENDPOINT)


def _fake_hf_hub_modules(*, endpoint: str = "https://huggingface.co") -> tuple[Any, Any]:
    """Stub replacements for the two submodules whose state matters here."""
    constants = types.SimpleNamespace(
        ENDPOINT=endpoint,
        HUGGINGFACE_CO_URL_TEMPLATE=endpoint + "/{repo_id}/resolve/{revision}/{filename}",
    )
    api = types.SimpleNamespace(endpoint=endpoint)
    hf_api_mod = types.SimpleNamespace(api=api)
    return constants, hf_api_mod


def test_login_calls_hf_login_when_hub_not_yet_imported(monkeypatch: pytest.MonkeyPatch) -> None:
    """Neither submodule has been imported: nothing to retarget, so the normal
    huggingface_hub.login() call goes ahead."""
    monkeypatch.delitem(sys.modules, "huggingface_hub.constants", raising=False)
    monkeypatch.delitem(sys.modules, "huggingface_hub.hf_api", raising=False)
    fake_login = Mock()
    monkeypatch.setattr("huggingface_hub.login", fake_login, raising=False)

    thinkingface.login("http://localhost:8080", token="tf_xxx")

    fake_login.assert_called_once_with(token="tf_xxx", add_to_git_credential=False)
    assert os.environ["HF_ENDPOINT"] == "http://localhost:8080"
    assert os.environ["HF_TOKEN"] == "tf_xxx"


def test_login_retargets_already_imported_hub_and_calls_hf_login(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """huggingface_hub was already imported against huggingface.co: login()
    patches its already-built ENDPOINT/singleton in place and, having verified
    that took effect, proceeds to call huggingface_hub.login() as usual."""
    constants, hf_api_mod = _fake_hf_hub_modules(endpoint="https://huggingface.co")
    monkeypatch.setitem(sys.modules, "huggingface_hub.constants", constants)
    monkeypatch.setitem(sys.modules, "huggingface_hub.hf_api", hf_api_mod)
    fake_login = Mock()
    monkeypatch.setattr("huggingface_hub.login", fake_login, raising=False)

    thinkingface.login("http://localhost:8080", token="tf_xxx")

    fake_login.assert_called_once_with(token="tf_xxx", add_to_git_credential=False)
    assert constants.ENDPOINT == "http://localhost:8080"
    assert constants.HUGGINGFACE_CO_URL_TEMPLATE.startswith("http://localhost:8080/")
    assert hf_api_mod.api.endpoint == "http://localhost:8080"


def test_login_never_calls_hf_login_when_retarget_cannot_be_verified(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """The high-value case: if the already-imported huggingface_hub internals
    cannot be verified to now point at the thinkingface endpoint (e.g. an
    unrecognised internal shape on some other huggingface_hub version), the
    token must never be handed to huggingface_hub.login() -- that would send it
    to whatever endpoint (huggingface.co, by default) those internals still
    hold. login() must raise instead of silently leaking it."""
    constants, hf_api_mod = _fake_hf_hub_modules(endpoint="https://huggingface.co")

    # Simulate a singleton whose `endpoint` attribute cannot actually be
    # changed (a read-only property in some hypothetical huggingface_hub
    # version) -- the write is accepted but has no effect, which is exactly
    # the situation the post-write verification exists to catch.
    class _StuckApi:
        @property
        def endpoint(self) -> str:
            return "https://huggingface.co"

        @endpoint.setter
        def endpoint(self, value: str) -> None:
            pass  # silently ignored, like a stale cached property would be

    hf_api_mod.api = _StuckApi()
    monkeypatch.setitem(sys.modules, "huggingface_hub.constants", constants)
    monkeypatch.setitem(sys.modules, "huggingface_hub.hf_api", hf_api_mod)
    fake_login = Mock()
    monkeypatch.setattr("huggingface_hub.login", fake_login, raising=False)

    with pytest.raises(RuntimeError, match="already imported"):
        thinkingface.login("http://localhost:8080", token="tf_xxx")

    fake_login.assert_not_called()
    # The environment is still configured for this process even though the
    # already-built huggingface_hub singleton could not be fixed up.
    assert os.environ["HF_ENDPOINT"] == "http://localhost:8080"
    assert os.environ["HF_TOKEN"] == "tf_xxx"


def test_login_without_token_never_touches_hf_login(monkeypatch: pytest.MonkeyPatch) -> None:
    """No token means nothing to leak: login() must not call huggingface_hub's
    login (or raise) regardless of what state huggingface_hub is in."""
    constants, hf_api_mod = _fake_hf_hub_modules(endpoint="https://huggingface.co")
    monkeypatch.setitem(sys.modules, "huggingface_hub.constants", constants)
    monkeypatch.setitem(sys.modules, "huggingface_hub.hf_api", hf_api_mod)
    fake_login = Mock()
    monkeypatch.setattr("huggingface_hub.login", fake_login, raising=False)

    thinkingface.login("http://localhost:8080")

    fake_login.assert_not_called()
    assert os.environ["HF_ENDPOINT"] == "http://localhost:8080"
    assert "HF_TOKEN" not in os.environ
    # Still retargeted: an HfApi built before this call would otherwise send
    # whatever token huggingface_hub has cached to the old endpoint.
    assert constants.ENDPOINT == "http://localhost:8080"
    assert hf_api_mod.api.endpoint == "http://localhost:8080"


def test_login_retargets_frozen_endpoint_copies_in_other_submodules(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """huggingface_hub.utils._git_credential does `from ..constants import
    ENDPOINT`, so it keeps its own copy -- and it is what
    login(add_to_git_credential=True) stores the credential for. Left stale,
    the thinkingface token would be saved as a huggingface.co git credential."""
    constants, hf_api_mod = _fake_hf_hub_modules(endpoint="https://huggingface.co")
    git_credential = types.SimpleNamespace(ENDPOINT="https://huggingface.co")
    monkeypatch.setitem(sys.modules, "huggingface_hub.constants", constants)
    monkeypatch.setitem(sys.modules, "huggingface_hub.hf_api", hf_api_mod)
    monkeypatch.setitem(sys.modules, "huggingface_hub.utils._git_credential", git_credential)
    fake_login = Mock()
    monkeypatch.setattr("huggingface_hub.login", fake_login, raising=False)

    thinkingface.login("http://localhost:8080", token="tf_xxx", add_to_git_credential=True)

    assert git_credential.ENDPOINT == "http://localhost:8080"
    fake_login.assert_called_once_with(token="tf_xxx", add_to_git_credential=True)


def test_login_refuses_when_a_frozen_endpoint_copy_cannot_be_retargeted(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    constants, hf_api_mod = _fake_hf_hub_modules(endpoint="https://huggingface.co")

    class _StuckModule:
        @property
        def ENDPOINT(self) -> str:  # noqa: N802 - mirrors the module attribute
            return "https://huggingface.co"

        @ENDPOINT.setter
        def ENDPOINT(self, value: str) -> None:  # noqa: N802
            pass

    monkeypatch.setitem(sys.modules, "huggingface_hub.constants", constants)
    monkeypatch.setitem(sys.modules, "huggingface_hub.hf_api", hf_api_mod)
    monkeypatch.setitem(sys.modules, "huggingface_hub.utils._git_credential", _StuckModule())
    fake_login = Mock()
    monkeypatch.setattr("huggingface_hub.login", fake_login, raising=False)

    with pytest.raises(RuntimeError, match="already imported"):
        thinkingface.login("http://localhost:8080", token="tf_xxx", add_to_git_credential=True)

    fake_login.assert_not_called()
