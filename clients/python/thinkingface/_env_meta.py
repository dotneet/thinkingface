"""Best-effort collection of run environment metadata for ``thinkingface.trackio``.

Everything gathered here exists to answer "what code, on what machine,
produced this run" without the caller having to remember to put it in
``config`` by hand -- mirroring the value MLflow's autolog gets from doing
the same thing (see ``todo/exp-run-env-metadata.md``).

Every individual collector is best-effort: a missing ``git`` binary, a
script run outside a repo, the absence of ``torch``/``nvidia-smi``, or any
other environmental quirk simply drops that one key. Nothing in this module
may raise past :func:`collect` -- ``trackio.init()`` must never fail because
of the environment it happens to run in.

Set ``THINKINGFACE_META=off`` to disable this collection entirely (see
:func:`is_disabled`).
"""

from __future__ import annotations

import hashlib
import platform
import re
import socket
import subprocess
import sys
from importlib import metadata
from os import environ
from typing import Any

_SUBPROCESS_TIMEOUT_SECONDS = 3.0

# Substrings (after stripping leading -/-- and any -/_ separators, lowercased)
# that mark a CLI flag as carrying a secret value. Matched as a substring
# rather than an exact name so that variants like --hf-token, --auth-token,
# --api-key, --api_key and single-word forms like --apikey all get caught.
# False positives (e.g. "--author") are an acceptable trade-off: leaking a
# credential into a stored run is worse than over-masking one argument.
_SENSITIVE_SUBSTRINGS = (
    "token",
    "password",
    "passwd",
    "pwd",
    "secret",
    "auth",
    "credential",
    "apikey",
    "accesskey",
    "privatekey",
)
_MASK = "***"

__all__ = ["is_disabled", "mask_cmdline", "collect"]


def is_disabled() -> bool:
    """Whether ``THINKINGFACE_META=off`` opts out of metadata collection."""
    return environ.get("THINKINGFACE_META", "").strip().lower() == "off"


def _looks_sensitive(flag_name: str) -> bool:
    normalized = re.sub(r"[-_]", "", flag_name.lstrip("-").lower())
    return any(keyword in normalized for keyword in _SENSITIVE_SUBSTRINGS)


def mask_cmdline(argv: list[str]) -> list[str]:
    """Mask the values of secret-looking flags in an argv list.

    Handles both the two-argument form (``--token secret``) and the
    single-argument ``=`` form (``--token=secret``). A flag is considered
    sensitive if its name contains a keyword like ``token``, ``password``,
    ``secret``, ``auth``, ``credential`` or ``api-key`` (case-insensitive,
    ``-``/``_`` separators ignored). Positional (non-flag) arguments are
    left untouched.
    """
    masked: list[str] = []
    mask_next = False
    for arg in argv:
        if mask_next:
            masked.append(_MASK)
            mask_next = False
            continue

        if not arg.startswith("-"):
            masked.append(arg)
            continue

        name, sep, _value = arg.partition("=")
        if sep:
            masked.append(f"{name}={_MASK}" if _looks_sensitive(name) else arg)
            continue

        masked.append(arg)
        if _looks_sensitive(arg):
            mask_next = True

    return masked


def _run_git(*args: str) -> str | None:
    result = subprocess.run(
        ["git", *args],
        capture_output=True,
        text=True,
        timeout=_SUBPROCESS_TIMEOUT_SECONDS,
    )
    if result.returncode != 0:
        return None
    return result.stdout.strip()


def _collect_git() -> dict[str, Any]:
    """Git commit/branch/dirty state of the current working directory.

    Returns {} silently when git is not installed, the process is not
    inside a repository, or anything else about the environment prevents
    reading this (permissions, a git worktree in a weird state, etc).
    """
    try:
        commit = _run_git("rev-parse", "HEAD")
        if not commit:
            return {}
        info: dict[str, Any] = {"commit": commit}

        branch = _run_git("rev-parse", "--abbrev-ref", "HEAD")
        if branch:
            info["branch"] = branch

        status = _run_git("status", "--porcelain")
        if status is not None:
            info["dirty"] = bool(status)

        return info
    except Exception:
        return {}


def _collect_gpu() -> dict[str, Any]:
    """GPU name/count/CUDA version, via torch if available, else nvidia-smi."""
    try:
        import torch  # type: ignore[import-not-found]

        if torch.cuda.is_available():
            count = torch.cuda.device_count()
            info: dict[str, Any] = {"count": count}
            if count > 0:
                info["name"] = torch.cuda.get_device_name(0)
            cuda_version = getattr(torch.version, "cuda", None)
            if cuda_version:
                info["cuda"] = cuda_version
            return info
        return {}
    except Exception:
        pass

    try:
        names_result = subprocess.run(
            ["nvidia-smi", "--query-gpu=name", "--format=csv,noheader"],
            capture_output=True,
            text=True,
            timeout=_SUBPROCESS_TIMEOUT_SECONDS,
        )
        if names_result.returncode != 0:
            return {}
        names = [line.strip() for line in names_result.stdout.splitlines() if line.strip()]
        if not names:
            return {}
        info = {"name": names[0], "count": len(names)}

        smi_result = subprocess.run(
            ["nvidia-smi"],
            capture_output=True,
            text=True,
            timeout=_SUBPROCESS_TIMEOUT_SECONDS,
        )
        if smi_result.returncode == 0:
            match = re.search(r"CUDA Version:\s*([\d.]+)", smi_result.stdout)
            if match:
                info["cuda"] = match.group(1)
        return info
    except Exception:
        return {}


def _collect_requirements_sha256() -> str | None:
    """SHA-256 of the sorted "name==version" list of installed distributions.

    This is the "pip freeze equivalent" from the design doc: the full text
    is deliberately not sent (see todo/exp-run-artifacts.md for that), just
    a stable hash so two runs can be compared for "same environment or not"
    without shelling out to pip (which may not even be installed / importable
    in the training environment).
    """
    try:
        packages = sorted(
            f"{dist.metadata['Name']}=={dist.version}"
            for dist in metadata.distributions()
            if dist.metadata.get("Name")
        )
        if not packages:
            return None
        return hashlib.sha256("\n".join(packages).encode("utf-8")).hexdigest()
    except Exception:
        return None


def collect() -> dict[str, Any]:
    """Collect everything this module knows how to, best-effort.

    Returns a dict meant for ``config["_meta"] = ...``. Each collector is
    independently wrapped, so one failing (or finding nothing) never drops
    the others, and this function itself never raises.
    """
    meta: dict[str, Any] = {}

    try:
        git_info = _collect_git()
        if git_info:
            meta["git"] = git_info
    except Exception:
        pass

    try:
        meta["cmdline"] = mask_cmdline(sys.argv)
    except Exception:
        pass

    try:
        meta["python"] = sys.version.split()[0]
    except Exception:
        pass

    try:
        meta["platform"] = platform.platform()
    except Exception:
        pass

    try:
        meta["hostname"] = socket.gethostname()
    except Exception:
        pass

    try:
        gpu_info = _collect_gpu()
        if gpu_info:
            meta["gpu"] = gpu_info
    except Exception:
        pass

    try:
        requirements_sha256 = _collect_requirements_sha256()
        if requirements_sha256:
            meta["requirements_sha256"] = requirements_sha256
    except Exception:
        pass

    return meta
