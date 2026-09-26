"""Python client helpers for a self-hosted thinkingface Hub.

thinkingface implements the subset of the HuggingFace Hub HTTP API that
``huggingface_hub`` and ``datasets`` actually use, so those libraries work
unmodified once ``HF_ENDPOINT`` points at a thinkingface server. This
package is a thin convenience layer on top of that: :func:`login` wires up
the environment, and :mod:`thinkingface.trackio` provides a trackio-compatible
shim for real-time experiment logging.

Typical usage::

    import thinkingface

    thinkingface.login("http://localhost:8080", token="tf_xxx")

    from huggingface_hub import HfApi, upload_file

    api = HfApi()
    api.create_repo("me/my-dataset", repo_type="dataset")
    upload_file(
        path_or_fileobj="README.md",
        path_in_repo="README.md",
        repo_id="me/my-dataset",
        repo_type="dataset",
    )

Call :func:`login` *before* importing anything from ``huggingface_hub`` or
``datasets``. Both resolve their default endpoint once, the first time their
own internals are imported (``huggingface_hub``'s ``constants`` / ``hf_api``
submodules; ``datasets``'s ``config`` submodule), so importing either first
and calling :func:`login` afterwards can leave it talking to huggingface.co
with your thinkingface token. See :func:`login` for what happens (and how it
is guarded against) when that order is not followed.
"""

from __future__ import annotations

import os
import sys
import warnings

__version__ = "0.1.0"

__all__ = ["login", "whoami"]


def _retarget_imported_hf_hub(endpoint: str) -> bool:
    """Best-effort: if ``huggingface_hub`` internals were already built against
    the old endpoint, retarget them at ``endpoint``.

    ``huggingface_hub`` reads ``HF_ENDPOINT`` exactly once: when its
    ``constants`` submodule is first imported, into ``constants.ENDPOINT``.
    Its module-level ``HfApi`` singleton (``huggingface_hub.hf_api.api``, the
    object backing the top-level ``login`` / ``whoami`` / ``create_repo`` /
    ``upload_file`` / ... functions) captures that value into its own
    ``endpoint`` attribute at construction time and never re-reads it.
    Setting ``os.environ["HF_ENDPOINT"]`` after either has already been
    imported therefore changes nothing they use.

    If neither submodule has been imported yet, there is nothing to fix: the
    environment variable this function's caller just set will be picked up
    correctly whenever they are. If one or both have been imported, this
    patches them in place and reports whether every piece that matters now
    agrees on ``endpoint``.

    Returns:
        True if ``endpoint`` is (or will be) the effective endpoint for
        ``huggingface_hub``'s top-level functions; False if it could not be
        verified, in which case the caller must not do anything that sends
        the thinkingface token to whatever endpoint is actually in effect.
    """
    constants = sys.modules.get("huggingface_hub.constants")
    hf_api_mod = sys.modules.get("huggingface_hub.hf_api")
    # Submodules that did `from ..constants import ENDPOINT` hold their own
    # frozen copy -- utils._git_credential is one, and it is what
    # login(add_to_git_credential=True) writes the credential for, so leaving
    # it stale stores the thinkingface token as a huggingface.co credential.
    # Rather than track which submodules do this on which version, every
    # loaded huggingface_hub module with a string ENDPOINT is retargeted and
    # checked.
    copies = [
        mod
        for name, mod in list(sys.modules.items())
        if (name == "huggingface_hub" or name.startswith("huggingface_hub."))
        and mod is not None
        and isinstance(getattr(mod, "ENDPOINT", None), str)
    ]
    if constants is None and hf_api_mod is None and not copies:
        return True  # nothing built yet against the old endpoint

    try:
        for mod in copies:
            mod.ENDPOINT = endpoint
        if constants is not None:
            constants.ENDPOINT = endpoint
            # Rebuilt from ENDPOINT the same way huggingface_hub.constants
            # itself derives it at import time; used by resolve/download URLs.
            if hasattr(constants, "HUGGINGFACE_CO_URL_TEMPLATE"):
                constants.HUGGINGFACE_CO_URL_TEMPLATE = (
                    endpoint + "/{repo_id}/resolve/{revision}/{filename}"
                )
        if hf_api_mod is not None and hasattr(hf_api_mod, "api"):
            hf_api_mod.api.endpoint = endpoint
    except Exception:  # unknown internal shape on this huggingface_hub version
        return False

    if constants is not None and getattr(constants, "ENDPOINT", None) != endpoint:
        return False
    if any(getattr(mod, "ENDPOINT", None) != endpoint for mod in copies):
        return False
    if (
        hf_api_mod is not None
        and hasattr(hf_api_mod, "api")
        and getattr(hf_api_mod.api, "endpoint", None) != endpoint
    ):
        return False
    return True


def _retarget_imported_datasets(endpoint: str) -> bool:
    """Best-effort: if ``datasets`` internals were already built against the
    old endpoint, retarget them at ``endpoint``.

    ``datasets`` freezes its own copy of the endpoint independently of
    ``huggingface_hub``: ``datasets/config.py`` reads ``HF_ENDPOINT`` once, at
    import time, into ``datasets.config.HF_ENDPOINT``, and derives
    ``HUB_DATASETS_URL`` from it in that same statement
    (``HUB_DATASETS_URL = HF_ENDPOINT + "/datasets/{repo_id}/resolve/{revision}/{path}"``).
    Every call site across the package -- ``load_dataset`` (``load.py``),
    ``hub.py``, ``data_files.py``, ``arrow_dataset.py``, ``dataset_dict.py``,
    the ``features/*`` modules, ``utils/file_utils.py``, ... -- builds its own
    ``HfApi(endpoint=config.HF_ENDPOINT, token=...)`` /
    ``HfFileSystem(endpoint=config.HF_ENDPOINT, ...)`` per call rather than
    caching one, so retargeting ``config.HF_ENDPOINT`` (and the derived
    ``HUB_DATASETS_URL``) covers them without needing a singleton fixup like
    ``huggingface_hub.hf_api.api``. Left unpatched, ``import datasets`` before
    ``thinkingface.login(url, token=...)`` would make a later
    ``load_dataset("me/ds")`` send the thinkingface token to huggingface.co.
    (``HUB_DATASETS_HFFS_URL`` is a fixed ``hf://datasets/...`` URI template
    that does not depend on ``HF_ENDPOINT``, so there is nothing to patch
    there.)

    Also retargets any other loaded ``datasets.*`` submodule holding its own
    string ``HF_ENDPOINT`` copy (``from ... import HF_ENDPOINT`` style),
    mirroring ``_retarget_imported_hf_hub``'s handling of
    ``huggingface_hub.utils._git_credential`` -- none exist in the currently
    installed version, but nothing here should rely on that staying true.

    Returns:
        True if ``datasets`` was never imported, or if it was and every piece
        found now agrees on ``endpoint``; False if it could not be verified,
        in which case the caller must not do anything that sends the
        thinkingface token to whatever endpoint ``datasets`` still has
        cached.
    """
    config = sys.modules.get("datasets.config")
    copies = [
        mod
        for name, mod in list(sys.modules.items())
        if (name == "datasets" or name.startswith("datasets."))
        and name != "datasets.config"
        and mod is not None
        and isinstance(getattr(mod, "HF_ENDPOINT", None), str)
    ]
    if config is None and not copies:
        return True  # nothing built yet against the old endpoint

    try:
        for mod in copies:
            mod.HF_ENDPOINT = endpoint
        if config is not None:
            config.HF_ENDPOINT = endpoint
            if hasattr(config, "HUB_DATASETS_URL"):
                # Rebuilt from HF_ENDPOINT the same way datasets/config.py
                # itself derives it at import time.
                config.HUB_DATASETS_URL = endpoint + "/datasets/{repo_id}/resolve/{revision}/{path}"
    except Exception:  # unknown internal shape on this datasets version
        return False

    if config is not None and getattr(config, "HF_ENDPOINT", None) != endpoint:
        return False
    if (
        config is not None
        and hasattr(config, "HUB_DATASETS_URL")
        and not str(getattr(config, "HUB_DATASETS_URL", "")).startswith(endpoint)
    ):
        return False
    if any(getattr(mod, "HF_ENDPOINT", None) != endpoint for mod in copies):
        return False
    return True


def login(
    endpoint: str,
    token: str | None = None,
    *,
    add_to_git_credential: bool = False,
) -> None:
    """Point the ``huggingface_hub`` ecosystem at a thinkingface server.

    Sets ``HF_ENDPOINT`` (and, when a token is given, ``HF_TOKEN``) plus
    ``HF_HUB_DISABLE_XET`` for the current process so that
    ``huggingface_hub``, ``datasets`` and the ``hf`` CLI all transparently
    talk to ``endpoint`` instead of huggingface.co.

    Call this **before** importing anything from ``huggingface_hub`` or
    ``datasets``. Both resolve their default endpoint once, at import time
    (``huggingface_hub`` into ``huggingface_hub.constants.ENDPOINT`` and the
    module-level ``HfApi`` singleton that backs its top-level ``login`` /
    ``whoami`` / ``create_repo`` / ``upload_file`` / ... functions;
    ``datasets`` into ``datasets.config.HF_ENDPOINT`` and the
    ``HUB_DATASETS_URL`` template derived from it, read by ``load_dataset``
    and every ``push_to_hub`` / ``save_to_disk`` counterpart), and setting
    ``HF_ENDPOINT`` afterwards does not change anything already built. If
    either turns out to already be imported, this makes a best-effort
    attempt to retarget those already-built internals at ``endpoint``; if
    that cannot be verified for either one, it skips calling
    ``huggingface_hub.login()`` rather than risk sending ``token`` to
    whatever endpoint (e.g. huggingface.co) is still actually in effect, and
    raises so the mistake is caught immediately rather than silently leaking
    the token on the first real request.

    Args:
        endpoint: Base URL of the thinkingface server, e.g.
            ``"http://localhost:8080"`` or ``"https://hub.internal.example.com"``.
        token: A thinkingface access token (``tf_...``). Also usable as an
            ``HF_TOKEN`` / git Basic-auth password / ``Authorization: Bearer``
            value, since thinkingface treats them identically. When provided,
            this also calls ``huggingface_hub.login()`` so credentials are
            cached the same way the official ``hf auth login`` would.
        add_to_git_credential: Forwarded to ``huggingface_hub.login()``.

    Raises:
        RuntimeError: ``token`` was given, but ``huggingface_hub`` and/or
            ``datasets`` were already imported and their endpoint could not
            be safely retargeted, so calling ``huggingface_hub.login()``
            would risk sending ``token`` to the wrong server. ``HF_TOKEN`` is
            deliberately left unset in this case (only ``HF_ENDPOINT`` is
            set), so no later call can pick up the token and send it to the
            wrong endpoint.
    """
    endpoint = endpoint.rstrip("/")
    os.environ["HF_ENDPOINT"] = endpoint
    # thinkingface moves large files over Git LFS, not Xet. huggingface_hub >= 1.0
    # reaches for Xet whenever the hf_xet package is installed, which would fail
    # against this server, so disable it unless the caller insisted otherwise.
    os.environ.setdefault("HF_HUB_DISABLE_XET", "1")

    # Retargeted whether or not a token was given: without one, an HfApi
    # built before this call would still send whatever token huggingface_hub
    # has cached (possibly a thinkingface one from an earlier login) to the
    # old endpoint. Only the raise depends on the token, since that is the
    # case where this call itself would be the one to leak it.
    endpoint_effective = _retarget_imported_hf_hub(endpoint) and _retarget_imported_datasets(
        endpoint
    )
    if token:
        if not endpoint_effective:
            # Deliberately raised *before* os.environ["HF_TOKEN"] is set: this
            # process may keep running after the exception (a notebook kernel
            # catching it and retrying some other call), and get_token() reads
            # HF_TOKEN on every call. Setting it here would leak the token to
            # whatever endpoint (huggingface.co, by default) huggingface_hub
            # or datasets are still actually pointed at, on the very first
            # later call that doesn't go through this function.
            raise RuntimeError(
                "thinkingface.login: huggingface_hub and/or datasets were "
                "already imported before this call, and their endpoint could "
                "not be safely retargeted. Calling huggingface_hub.login() "
                "now would risk sending your thinkingface token to the wrong "
                "server (huggingface.co by default), so it was skipped. Call "
                "thinkingface.login(...) before importing anything from "
                "huggingface_hub or datasets (see this module's docstring), "
                "or set HF_ENDPOINT in the environment before your process "
                "starts. HF_ENDPOINT is still set for this process, but "
                "HF_TOKEN was deliberately left unset -- setting it would let "
                "a later call send the thinkingface token to whatever "
                "endpoint the already-built huggingface_hub/datasets objects "
                "are still pointed at."
            )
        os.environ["HF_TOKEN"] = token
        try:
            from huggingface_hub import login as _hf_login

            _hf_login(token=token, add_to_git_credential=add_to_git_credential)
        except Exception as exc:  # pragma: no cover - best-effort convenience
            warnings.warn(
                "thinkingface.login: huggingface_hub.login() failed "
                f"({exc!r}); HF_ENDPOINT/HF_TOKEN are still set for this "
                "process, so most operations will still work."
            )


def whoami(endpoint: str | None = None, token: str | None = None) -> dict:
    """Convenience wrapper around ``huggingface_hub.whoami()``.

    Uses the currently configured ``HF_ENDPOINT``/``HF_TOKEN`` unless
    explicit overrides are passed.
    """
    from huggingface_hub import HfApi

    api = HfApi(endpoint=endpoint or os.environ.get("HF_ENDPOINT"), token=token)
    return api.whoami()
