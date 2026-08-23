"""Staging rules for ``trackio.log_artifact``.

Artifacts are not a store of their own. A run's files go into the same
thinkingface *dataset* repository its metrics do, under a fixed path::

    {project}/artifacts/{run}/{name}

and are uploaded through the ordinary HuggingFace-compatible
preupload/commit endpoints, so they end up git-versioned, mirrored into
``exports/`` and included in a ``git clone`` -- with large files routed to
LFS by the repository's ``.gitattributes`` like any other upload. There is
no artifact API and nothing here talks to the network: this module only
decides *what* to upload and *where to put it*, which is the part worth
unit-testing.

See docs/api-contract.md §7 for the naming convention this file fixes.
"""

from __future__ import annotations

import os
from pathlib import Path

# The directory segment that separates artifacts from the parquet layout the
# server's indexer scans (backend/internal/experiments/layout.go).
ARTIFACTS_DIR = "artifacts"

# Names the indexer would mistake for a project of its own if they appeared
# inside an artifact directory: it reads "{dir}/metrics.parquet" as a project
# called "{dir}", which for an artifact would be the bogus project
# "{project}/artifacts/{run}". Rejected rather than silently renamed, so the
# caller learns to pass an explicit ``name=``.
RESERVED_ARTIFACT_NAMES = frozenset({"metrics.parquet"})

# Ceiling on how many files one log_artifact(directory) call may stage. A run
# pointing at, say, a checkpoint directory of a thousand shards should be
# pushing a model repository instead, and a single commit of that size is not
# what this path is for.
MAX_FILES_PER_ARTIFACT = 500


def normalize_artifact_name(name: str) -> str:
    """Validate the in-repository name of one artifact.

    Subdirectories are allowed (``"plots/confusion.png"``); anything that
    could climb out of the run's own directory, or that collides with the
    parquet layout, is a ``ValueError``.
    """
    cleaned = name.strip().replace("\\", "/").strip("/")
    if not cleaned:
        raise ValueError("artifact name must not be empty")
    parts = [p for p in cleaned.split("/") if p not in ("", ".")]
    if not parts:
        raise ValueError(f"artifact name must not be empty, got {name!r}")
    if ".." in parts:
        raise ValueError(f"artifact name must not contain '..', got {name!r}")
    normalized = "/".join(parts)
    if normalized.lower() in RESERVED_ARTIFACT_NAMES:
        raise ValueError(
            f"artifact name {normalized!r} is reserved: the server reads a file with "
            "that name as an experiment's metrics table. Pass an explicit name=, "
            'e.g. name="eval/metrics.parquet".'
        )
    return normalized


def artifact_path(project: str, run: str, name: str) -> str:
    """Where one artifact lands inside the experiment dataset repository."""
    return f"{project}/{ARTIFACTS_DIR}/{run}/{name}"


def stage(path: str | os.PathLike[str], name: str | None = None) -> list[tuple[Path, str]]:
    """Expand one ``log_artifact`` call into (local file, in-repo name) pairs.

    A file stages as itself; a directory stages every file beneath it, keeping
    the relative layout under ``name`` (which defaults to the directory's own
    name, as wandb's ``log_artifact`` does). Symlinks are followed for files
    but never walked into as directories, so a link pointing back up the tree
    cannot turn into an unbounded upload.
    """
    source = Path(path)
    base = normalize_artifact_name(name if name is not None else source.name)

    if source.is_dir():
        out: list[tuple[Path, str]] = []
        for child in sorted(source.rglob("*")):
            if child.is_dir() or child.is_symlink():
                continue
            relative = child.relative_to(source).as_posix()
            out.append((child, normalize_artifact_name(f"{base}/{relative}")))
        if not out:
            raise ValueError(f"artifact directory {source}: no files to upload")
        if len(out) > MAX_FILES_PER_ARTIFACT:
            raise ValueError(
                f"artifact directory {source} holds {len(out)} files, more than the "
                f"{MAX_FILES_PER_ARTIFACT} one log_artifact() call uploads"
            )
        return out

    if not source.is_file():
        raise ValueError(f"artifact {source}: not a file or directory")
    return [(source, base)]
