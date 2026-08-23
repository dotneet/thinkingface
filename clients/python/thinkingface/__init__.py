"""Python client helpers for a self-hosted thinkingface Hub.

thinkingface implements the subset of the HuggingFace Hub HTTP API that
``huggingface_hub`` and ``datasets`` actually use, so those libraries work
unmodified once ``HF_ENDPOINT`` points at a thinkingface server. This
package is a thin convenience layer on top of that: :func:`login` wires up
the environment, and :mod:`thinkingface.trackio` provides a trackio-compatible
shim for real-time experiment logging.

Typical usage::

    import thinkingface
    from huggingface_hub import HfApi, upload_file

    thinkingface.login("http://localhost:8080", token="tf_xxx")

    api = HfApi()
    api.create_repo("me/my-dataset", repo_type="dataset")
    upload_file(
        path_or_fileobj="README.md",
        path_in_repo="README.md",
        repo_id="me/my-dataset",
        repo_type="dataset",
    )
"""

from __future__ import annotations

import os
import warnings

__version__ = "0.1.0"

__all__ = ["login", "whoami"]


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

    Args:
        endpoint: Base URL of the thinkingface server, e.g.
            ``"http://localhost:8080"`` or ``"https://hub.internal.example.com"``.
        token: A thinkingface access token (``tf_...``). Also usable as an
            ``HF_TOKEN`` / git Basic-auth password / ``Authorization: Bearer``
            value, since thinkingface treats them identically. When provided,
            this also calls ``huggingface_hub.login()`` so credentials are
            cached the same way the official ``hf auth login`` would.
        add_to_git_credential: Forwarded to ``huggingface_hub.login()``.
    """
    endpoint = endpoint.rstrip("/")
    os.environ["HF_ENDPOINT"] = endpoint
    # thinkingface moves large files over Git LFS, not Xet. huggingface_hub >= 1.0
    # reaches for Xet whenever the hf_xet package is installed, which would fail
    # against this server, so disable it unless the caller insisted otherwise.
    os.environ.setdefault("HF_HUB_DISABLE_XET", "1")

    if token:
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
