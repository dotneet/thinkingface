"""On-disk run directories: offline mode and the online mode's spill files.

The format is a contract with ``tf experiments sync`` (the Go side replays
these directories), so it is fixed in docs/dev/agent-features.md §2.9 and
this module follows it to the letter:

    {THINKINGFACE_OFFLINE_DIR}/{UTC yyyymmddTHHMMSS}-{slug(project)}-{slug(run)}-{8 hex}/
        run.jsonl       append-only, one JSON object per line, flushed per line
        artifacts/      copies of the files log_artifact / media staged
        sync-state.json written only by the syncer

Every ``run.jsonl`` record carries ``"v": 1`` and a ``"type"`` of ``init``
(always the first line), ``log``, ``artifact``, ``model`` or ``finish``.

Nothing here raises into a training script on its own: callers catch
``OSError`` (and the ``ValueError`` of an unencodable record) and fall back
to warning, which is why every method lets those through rather than
swallowing them.
"""

from __future__ import annotations

import json
import os
import re
import secrets
import shutil
import threading
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

DEFAULT_DIR = "./thinkingface-offline"
RUN_FILE = "run.jsonl"
ARTIFACTS_DIR = "artifacts"
FORMAT_VERSION = 1

_SLUG_MAX_BYTES = 40
_SLUG_RE = re.compile(r"[^A-Za-z0-9._-]")


def base_dir() -> Path:
    """``THINKINGFACE_OFFLINE_DIR``, or ``./thinkingface-offline``."""
    return Path(os.environ.get("THINKINGFACE_OFFLINE_DIR") or DEFAULT_DIR)


def slug(text: str) -> str:
    """Keep ``[A-Za-z0-9._-]``, replace anything else with ``_``, cap at 40 bytes.

    The replacement is per character, so the result is pure ASCII and its
    length in bytes is its length in characters.
    """
    return _SLUG_RE.sub("_", text)[:_SLUG_MAX_BYTES]


def run_dir_name(project: str, run: str, now: datetime | None = None) -> str:
    stamp = (now or datetime.now(timezone.utc)).strftime("%Y%m%dT%H%M%S")
    return f"{stamp}-{slug(project)}-{slug(run)}-{secrets.token_hex(4)}"


def encode(record: dict[str, Any]) -> str:
    """One ``run.jsonl`` line (without the newline). Strict JSON: no NaN."""
    body = {"v": FORMAT_VERSION, **record}
    return json.dumps(body, ensure_ascii=False, allow_nan=False, separators=(",", ":"))


class RunDir:
    """One run directory, written to by the shim.

    Thread-safe: the flush timer, the caller's ``log_artifact`` and
    ``finish()`` can all append at once, and each line must land whole.
    """

    def __init__(self, path: Path) -> None:
        self.path = path
        self.run_file = path / RUN_FILE
        self._lock = threading.Lock()

    @classmethod
    def create(cls, project: str, run: str, parent: Path | None = None) -> RunDir:
        parent = parent if parent is not None else base_dir()
        path = parent / run_dir_name(project, run)
        path.mkdir(parents=True, exist_ok=False)
        return cls(path)

    def append(self, record: dict[str, Any]) -> None:
        """Append one record; raises ``ValueError`` if it cannot be encoded
        (nothing is written then) and ``OSError`` if the disk refuses it."""
        line = encode(record) + "\n"
        with self._lock, open(self.run_file, "a", encoding="utf-8") as fh:
            fh.write(line)
            fh.flush()

    def add_artifact(self, source: Path, name: str) -> None:
        """Copy ``source`` to ``artifacts/{name}`` and record it."""
        relative = f"{ARTIFACTS_DIR}/{name}"
        target = self.path / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, target)
        self.append({"type": "artifact", "name": name, "path": relative})

    def artifact_target(self, name: str) -> Path:
        """Where a file that is produced in place (a media PNG) should be
        written so that it ends up at ``artifacts/{name}``."""
        target = self.path / ARTIFACTS_DIR / name
        target.parent.mkdir(parents=True, exist_ok=True)
        return target

    def record_artifact(self, name: str) -> None:
        self.append({"type": "artifact", "name": name, "path": f"{ARTIFACTS_DIR}/{name}"})


def utc_now_iso() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")
