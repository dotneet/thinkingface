"""trackio-compatible shim that streams metrics straight to thinkingface.

Provides the same ``init`` / ``log`` / ``finish`` surface as `trackio`_ (and,
by extension, wandb), but instead of trackio's local SQLite store + batched
HuggingFace Dataset sync, this module posts directly to thinkingface's
real-time ingest API:

    POST /api/v1/experiments/{ns}/{repo}/{project}/log

This is "route B" from the design doc (docs/dev/thinkingface-design.md §8):
opt-in, low-latency logging, while the source of truth stays the same as
route A (trackio's own batch sync) -- a Parquet file inside a thinkingface
*dataset* repository, so the data is still git-versioned and readable via
``gcloud storage`` / DuckDB regardless of which path wrote it.

Environment variables:
    THINKINGFACE_ENDPOINT: Base URL of the thinkingface server
        (default ``http://localhost:8080``).
    THINKINGFACE_TOKEN: Access token (``tf_...``), sent as
        ``Authorization: Bearer``. Required for the ingest API (write scope).
    THINKINGFACE_REPO: Target dataset repo as ``namespace/name``
        (default ``{user}/trackio-metrics``, where ``{user}`` is resolved
        via ``GET /api/v1/me`` using ``THINKINGFACE_TOKEN``).
    THINKINGFACE_META: Set to ``off`` to disable the automatic run
        environment metadata collected by ``init()`` (see below).
    THINKINGFACE_SYSTEM_METRICS: Set to ``off`` to disable the periodic
        GPU/CPU/memory telemetry sampled in the background by every active
        run (see below).
    THINKINGFACE_MODE: ``online`` (default) or ``offline`` -- see below.
    THINKINGFACE_OFFLINE_DIR: Where offline runs and spilled points are
        written (default ``./thinkingface-offline``).
    THINKINGFACE_HEARTBEAT_SECS: Liveness interval declared to the server
        (default 30, at most 3600; ``0`` disables heartbeats).
    THINKINGFACE_ARTIFACT_INTERVAL: Seconds between background commits of
        staged artifacts and media (default 60; ``0`` holds them until
        ``save()`` / ``finish()``).

``init()`` also merges a best-effort snapshot of the run's environment into
``config`` under the reserved ``_meta`` key (git commit/branch/dirty state,
masked ``sys.argv``, Python/platform/hostname, GPU info, and a hash of
installed packages -- see ``thinkingface._env_meta``). This is collected
the same way MLflow's autolog records "what code produced this run"; it is
never allowed to raise, and any collector that fails or finds nothing is
silently omitted. Set ``THINKINGFACE_META=off`` to disable it entirely.

Every active run also piggybacks GPU/CPU/memory sampling onto its existing
flush timer (roughly every ``_system_metrics.DEFAULT_INTERVAL_SECONDS``,
10s by default) and logs the result under ``system/``-prefixed keys (e.g.
``system/gpu.0.util``, ``system/cpu.percent`` -- see
``thinkingface._system_metrics``). Like the env metadata above, this is
best-effort and never raises: a machine with no GPU and no ``psutil``
installed simply logs nothing under ``system/``. Set
``THINKINGFACE_SYSTEM_METRICS=off`` to disable it entirely.

``config`` may be a mapping, an ``argparse.Namespace`` or a dataclass, and
values JSON has no spelling for are converted when sent rather than costing
the run its whole config: ``Path`` -> str, ``Enum`` -> its value, dataclass /
namespace -> fields, numpy scalar / small array -> number / list, datetime ->
ISO 8601, set / tuple -> list, NaN / inf -> ``"nan"`` / ``"inf"``, and
anything else -> ``str(value)`` with one warning per run naming the keys
(see ``thinkingface.trackio._sanitize``).

``log_artifact(path, name=None)`` attaches a file (or a directory) to the
run. It goes into the same dataset repository as the metrics, under
``{project}/artifacts/{run}/{name}``, through the ordinary
preupload/commit endpoints -- so an artifact is git-versioned content,
reachable by ``git clone`` and, at its content-addressed bucket key, by
``gcloud storage cp`` (see the repository's ``GET .../gcs/{rev}`` API), with
large files routed to LFS by ``.gitattributes``. Staged artifacts are
committed together in the background every ``THINKINGFACE_ARTIFACT_INTERVAL``
seconds while anything is pending, immediately by ``save()``, and finally by
``finish()``.

``trackio.Image`` and ``trackio.Table`` values passed to ``log()`` are not
metrics: they are written to a PNG / parquet file and committed as the
artifacts ``media/{key}/step_{step:08d}.png`` and
``tables/{key}/step_{step:08d}.parquet`` (Pillow is optional -- needed only to
encode arrays and PIL images; a table needs pyarrow or pandas).

``log_model("ns/name", revision=None)`` records that the run produced that
model (resolving the repository's current HEAD when no revision is given).
The link is stored as a run *annotation*, so re-indexing the project's
parquet cannot erase it, and it shows up on both the run page and the
model's lineage view.

``init(group=..., job_type=...)`` records which sweep a run belongs to and
what role it played in it, the way wandb spells them. The run table folds a
group into one row and the parallel-coordinates view compares its members
axis by axis; a run that declares neither is listed flat, exactly as before.

``init(resume=...)`` decides what happens when the project already has a
run of that name -- ``"never"`` (the default) renames, ``"allow"`` continues
it, ``"must"`` continues it or raises. Continuing means steps carry on from
the server's ``last_step`` (online only -- see the offline mode below), the
status goes back to ``running``, and the configs are merged; see ``init`` for
the full contract.

A network failure never raises into the caller: points are logged as a
warning and kept for the next flush attempt, so a flaky connection or a
temporarily unreachable server does not abort a training run. The one
exception is ``resume="must"``, which cannot be honoured without reaching
the server and so raises rather than silently starting from zero.

``finish()`` is the exception to "next flush attempt": there is no next one,
since the background timer is being cancelled for good. It retries its own
final flush a few times with a short backoff before giving up, and only then
warns (loudly, with a count) that undelivered points are being dropped --
never leaving them to sit silently in a buffer nothing will ever flush again.
It also never posts ``/finish`` while a ``/log`` for the same run could still
be in flight, so a finished run's status can't be flipped back to running by
a stray late point.

Points the online mode gives up on -- evicted from a full retry buffer, or
left over when ``finish()`` runs out of retries against an unreachable or
failing server -- are not dropped any more but written to a *spill* run
directory under ``THINKINGFACE_OFFLINE_DIR`` (as are artifacts ``finish()``
could not commit), and the warning names the directory and the
``tf experiments sync`` command that delivers them. Points the server
*rejected* (a 4xx) are still dropped: they are bad data, not late data.
A spill record leaves the config out once the server already has it (so a
sync cannot roll a newer config back), and names no repository when
``THINKINGFACE_REPO`` was unset and the user could not be looked up (the sync
resolves ``{user}/trackio-metrics`` itself).
Only when the disk refuses them too are points dropped with a warning.

Every ``/log`` carries ``heartbeat_secs``, and a run that has posted nothing
for that long sends a point-less batch as a liveness ping from the flush
timer, so a long training step is not mistaken for a dead process
(docs/dev/agent-features.md §2.6).

``init(mode="offline")`` (or ``THINKINGFACE_MODE=offline``) makes no network
request at all: the run is written to
``{THINKINGFACE_OFFLINE_DIR}/{UTC time}-{project}-{run}-{hex}/`` -- points,
config, artifacts (copied), models and the final status, in the format of
docs/dev/agent-features.md §2.9 -- and ``tf experiments sync DIR`` uploads it
later, or follows it live with ``--watch``. Where the directory is, and the
command to sync it, is printed to stderr once when the run starts. An offline
``resume="allow"`` / ``"must"`` run does not continue the existing run's step
numbering -- the server's ``last_step`` is unknown offline, so auto-numbered
steps start at 0 (with a one-time warning); pass explicit ``step=`` if an
offline run continues an existing one.

.. _trackio: https://github.com/gradio-app/trackio
"""

from __future__ import annotations

import atexit
import json
import math
import numbers
import os
import shutil
import sys
import tempfile
import threading
import time
import warnings
from collections.abc import Mapping
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from urllib.parse import quote

import requests

from thinkingface import _env_meta, _system_metrics
from thinkingface.trackio import _artifacts, _media, _offline, _sanitize
from thinkingface.trackio._media import Image, Table

_DEFAULT_ENDPOINT = "http://localhost:8080"
_FLUSH_INTERVAL_SECONDS = 5.0
_FLUSH_MAX_POINTS = 100
_REQUEST_TIMEOUT_SECONDS = 10.0
# Ceiling on the retry buffer. A training run that logs for hours against an
# unreachable server would otherwise keep every point in memory forever; past
# this many points the oldest are dropped (with a warning) so logging can never
# be the thing that OOMs the run.
_BUFFER_MAX_POINTS = 10_000
# How many points one request may carry, matching the server's own ceiling
# (maxIngestPoints in internal/api/experiments.go, which rejects *more* than
# this with a 400). It is deliberately independent of _BUFFER_MAX_POINTS: the
# buffer is a memory bound and a full one plus a single further log() call is
# an ordinary situation, so flush() splits whatever it holds into requests of
# this size rather than betting that the two limits line up.
_MAX_POINTS_PER_REQUEST = 10_000
# Ceiling on how many distinct metric names one run may carry (maxIngestKeys,
# same file). The server keeps every key on the run forever, so once a run is
# over the line every later batch is rejected -- worth saying out loud on the
# client, where the offending name is still in view.
_MAX_METRIC_KEYS = 1000
# Ceiling on `group=` / `job_type=`, matching the server's own limit on the
# free-text ingest names (maxIngestNameBytes in internal/api/experiments.go).
_MAX_GROUPING_BYTES = 256
# Failures that mean the request body could never be built, as opposed to a
# request that was built and did not arrive. `requests` serialises `json=` with
# `allow_nan=False` and turns the resulting ValueError into an InvalidJSONError,
# and it lets a TypeError from a value json cannot encode at all through
# untouched -- both happen *before* the socket is touched, so retrying re-raises
# the identical exception forever. A malformed URL (MissingSchema / InvalidURL,
# both ValueError subclasses) is in the same category.
_ENCODE_ERRORS = (requests.exceptions.InvalidJSONError, TypeError, ValueError)
# finish() cannot rely on "the next flush" the way the timer-driven path can --
# it is about to cancel the timer for good -- so a retryable failure (5xx /
# network) on its own flush() gets a few more tries of its own, each a little
# further apart, before the run's final batch is given up on. Kept small: a
# training script calling finish() is waiting on this to return.
_FINISH_FLUSH_ATTEMPTS = 4
_FINISH_FLUSH_BACKOFF_SECONDS = 0.5
_FINISH_FLUSH_MAX_BACKOFF_SECONDS = 2.0
# Liveness pings (docs/dev/agent-features.md §2.6). Every /log carries
# heartbeat_secs, and a run that has posted nothing for that long sends a
# point-less batch so the server can tell "quiet" from "dead". The server
# accepts 1..3600; 0 (THINKINGFACE_HEARTBEAT_SECS=0) disables both.
_DEFAULT_HEARTBEAT_SECS = 30
_MAX_HEARTBEAT_SECS = 3600
# How often staged artifacts and media are committed while the run is still
# going (THINKINGFACE_ARTIFACT_INTERVAL). 0 or less holds them until save() /
# finish(), which is how every artifact used to be handled.
_DEFAULT_ARTIFACT_INTERVAL_SECONDS = 60.0
_MODES = ("online", "offline")

__all__ = [
    "Image",
    "Table",
    "finish",
    "init",
    "log",
    "log_artifact",
    "log_model",
    "save",
]


def _utc_now_iso() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def _is_nonfinite(value: Any) -> bool:
    """True for a NaN or an infinite metric value.

    Anything that is not a real number (a string, None, a tensor that was
    never reduced to a scalar) is *not* reported here: those are the server's
    business, and `integrations._numeric_only` already filters them out on the
    autolog paths.
    """
    if not isinstance(value, numbers.Real):
        return False
    try:
        return not math.isfinite(value)
    except (TypeError, ValueError, OverflowError):  # an exotic Real
        return False


def _finite_metrics(metrics: Any) -> tuple[dict[str, Any], list[str]]:
    """Split a metrics mapping into what can be sent and what cannot.

    JSON has no way to spell NaN or +/-inf, and this client sends strict JSON
    (`json.dumps(..., allow_nan=False)` is what `requests` does), so a single
    such value makes the whole batch unserialisable. Dropping the value is the
    only outcome that keeps the rest of the run's metrics flowing, and it
    matches what `integrations._numeric_only` already does with values the
    server cannot store.

    Returns the sendable metrics and the names that were dropped.
    """
    converted = dict(metrics)
    dropped = [name for name, value in converted.items() if _is_nonfinite(value)]
    if not dropped:
        return converted, []
    return {name: value for name, value in converted.items() if name not in dropped}, dropped


def _check_path_segment(label: str, value: str) -> None:
    """Reject values that cannot be a single URL path segment.

    quote() already keeps a stray `/` or `?` from escaping the segment, but a
    caller that passes one almost certainly meant something else, so fail loudly
    at init() rather than posting to a URL nobody intended.
    """
    if not value:
        raise ValueError(f"{label} must not be empty")
    if value in (".", "..") or "/" in value or "\\" in value:
        raise ValueError(f"{label} must be a single path segment, got {value!r}")


def _split_repo(repo: str) -> tuple[str, str]:
    """Split and validate a ``namespace/name`` repository reference."""
    try:
        namespace, repo_name = repo.split("/", 1)
    except ValueError as exc:
        raise ValueError(
            f'THINKINGFACE_REPO must look like "namespace/name", got {repo!r}'
        ) from exc
    _check_path_segment("namespace", namespace)
    _check_path_segment("repository name", repo_name)
    return namespace, repo_name


def _project_url(endpoint: str, namespace: str, repo_name: str, project: str) -> str:
    """Base URL of one project's experiment endpoints.

    quote() every segment: without it a project or repo name containing `/`,
    `..` or `?` would silently retarget the request at a different endpoint
    instead of failing.
    """
    return (
        f"{endpoint.rstrip('/')}/api/v1/experiments/{quote(namespace, safe='')}/"
        f"{quote(repo_name, safe='')}/{quote(project, safe='')}"
    )


def _env_number(name: str, default: float) -> float:
    """A numeric environment variable; a malformed one warns and uses ``default``."""
    raw = os.environ.get(name)
    if raw is None or not raw.strip():
        return default
    try:
        value = float(raw)
    except ValueError:
        warnings.warn(
            f"thinkingface.trackio: ignoring {name}={raw!r} (not a number); using {default}."
        )
        return default
    if not math.isfinite(value):
        return default
    return value


def _heartbeat_secs() -> int:
    """THINKINGFACE_HEARTBEAT_SECS, clamped to what the server accepts; 0 = off."""
    value = int(_env_number("THINKINGFACE_HEARTBEAT_SECS", _DEFAULT_HEARTBEAT_SECS))
    if value <= 0:
        return 0
    return min(value, _MAX_HEARTBEAT_SECS)


def _artifact_interval() -> float:
    """THINKINGFACE_ARTIFACT_INTERVAL in seconds; 0 or less = only save()/finish()."""
    return _env_number("THINKINGFACE_ARTIFACT_INTERVAL", _DEFAULT_ARTIFACT_INTERVAL_SECONDS)


def _normalize_mode(mode: Any) -> str:
    """``init(mode=...)``, else THINKINGFACE_MODE, else ``"online"``."""
    if mode is None:
        mode = os.environ.get("THINKINGFACE_MODE") or "online"
    text = str(mode).strip().lower()
    if text not in _MODES:
        raise ValueError(f'mode must be "online" or "offline", got {mode!r}')
    return text


def _sync_hint(path: Any) -> str:
    return f"upload with `tf experiments sync {path}`"


def _encodable_points(points: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """The points that strict JSON can encode (no NaN, no foreign types)."""
    kept = []
    for point in points:
        try:
            json.dumps(point, allow_nan=False)
        except (TypeError, ValueError):
            continue
        kept.append(point)
    return kept


def _append_log_record(
    run_dir: _offline.RunDir, points: list[dict[str, Any]], config: dict[str, Any] | None
) -> int:
    """Append one ``log`` record; returns how many points had to be left out
    because they cannot be encoded. Raises ``OSError`` when the disk does."""
    record: dict[str, Any] = {"type": "log", "points": points}
    if config is not None:
        record["config"] = config
    try:
        run_dir.append(record)
        return 0
    except (TypeError, ValueError):
        kept = _encodable_points(points)
        record["points"] = kept
        if config is not None:
            try:
                json.dumps(config, allow_nan=False)
            except (TypeError, ValueError):
                del record["config"]
        if kept or "config" in record:
            run_dir.append(record)
        return len(points) - len(kept)


class _Run:
    """A single active run: buffers points and flushes them periodically."""

    def __init__(
        self,
        endpoint: str,
        token: str | None,
        repo: str | None,
        project: str,
        name: str,
        config: dict[str, Any] | None,
        start_step: int = 0,
        resumed: bool = False,
        group: str = "",
        job_type: str = "",
        mode: str = "online",
        resume_mode: str = "never",
        repo_resolved: bool = True,
    ) -> None:
        self.endpoint = endpoint.rstrip("/")
        self.token = token
        self.repo = repo
        # False when THINKINGFACE_REPO was unset and GET /api/v1/me failed, so
        # `repo` is only the _FALLBACK_REPO placeholder. A spill directory
        # then says `"repo": null` and lets `tf experiments sync` resolve
        # "{user}/trackio-metrics" itself, instead of pointing the sync at a
        # repository that was never anybody's choice.
        self.repo_resolved = repo_resolved
        self.project = project
        self.name = name
        self.config = config or {}
        # "offline" writes the run to a directory for `tf experiments sync`
        # instead of the network (docs/dev/agent-features.md §2.9); "online"
        # talks to the server and only uses the disk for points it would
        # otherwise have had to drop (the "spill" directory, below).
        self.mode = mode
        self.offline = mode == "offline"
        self.resume_mode = resume_mode
        # The sweep this run belongs to and the role it played in it, as
        # wandb/trackio spell them. Sent with every batch rather than only
        # with the config: the server keeps the stored value when a batch
        # omits them, so repeating them costs two short strings and makes a
        # run that was created by an earlier attempt (or by a flush that
        # raced the first one) still land in the right group.
        self.group = group
        self.job_type = job_type
        # A resumed run picks up where the previous attempt stopped, so the
        # chart is one line rather than two overlapping ones; init() derives
        # this from the run's last_step (see the resume contract there).
        self.step = start_step
        self.resumed = resumed

        self._buffer: list[dict[str, Any]] = []
        self._lock = threading.Lock()
        # Serializes the actual network calls (flush()'s POST /log and
        # finish()'s POST /finish) across threads -- the background timer and
        # a caller's finish() can otherwise both be mid-request at once. Kept
        # separate from _lock (which only ever guards in-memory buffer state
        # and is held very briefly) so a send in flight never makes an
        # ordinary log() wait; only a log() that fills the buffer and flushes
        # inline queues behind it. Lock order is always _send_lock -> _lock.
        # See flush() and finish() for why the ordering this buys matters.
        self._send_lock = threading.Lock()
        self._finished = False
        # Every distinct metric name this run has logged. The server keeps a
        # run's keys forever and refuses a batch once there are more than
        # _MAX_METRIC_KEYS of them, at which point the run is permanently
        # un-loggable -- so the warning is raised here, once, while the name
        # that pushed it over is still the one in the caller's hand.
        self._metric_keys: set[str] = set()
        self._key_limit_warned = False
        # Warn once per run about NaN/inf metric values, not once per log()
        # call: a diverged loss produces one on every single step.
        self._nonfinite_warned = False
        # Whether the config has ever been sent, and (once it has) a snapshot
        # of exactly what was last sent -- so a later change to `self.config`
        # (e.g. `run.config.update(...)` from ThinkingFaceLightningLogger) is
        # detected and resent on the next flush, while an unchanged config is
        # not re-posted on every tick. See flush().
        self._config_sent = False
        self._last_sent_config: dict[str, Any] = {}
        # Warn once per run when a config value had to go through the
        # generic str() fallback of _sanitize (see _config_to_send).
        self._config_fallback_warned = False
        self._timer: threading.Timer | None = None
        # Set as soon as finish() starts, before _finished: from that moment
        # no heartbeat and no background artifact commit may begin.
        self._finishing = False

        # Liveness pings: see _maybe_heartbeat(). Counted from the run's
        # start, so a run that logs nothing for heartbeat_secs still shows up.
        self.heartbeat_secs = _heartbeat_secs()
        self._last_post_at = time.monotonic()
        self._last_heartbeat_attempt_at = self._last_post_at

        # Artifacts are gathered as they are logged and committed in batches:
        # in the background every _artifact_interval seconds while anything is
        # pending, on save(), and finally from finish(). Every commit is a git
        # commit, so batching keeps a run that saves twenty plots a minute to
        # one commit a minute. The model list is a wholesale replace and is
        # still sent once, from finish().
        self._artifacts: list[tuple[Any, str]] = []
        self._models: list[dict[str, str]] = []
        self._models_dirty = False
        self._artifact_interval = _artifact_interval()
        # Held for the whole of one commit, so the background uploader,
        # save() and finish() never commit at the same time. Never taken
        # while holding _send_lock or _lock (it takes _lock briefly itself).
        self._upload_lock = threading.Lock()
        self._upload_thread: threading.Thread | None = None
        self._upload_stop = threading.Event()
        # Media (trackio.Image / trackio.Table) is written to a temporary
        # directory this run owns, and each file is deleted once committed.
        self._media_dir: Path | None = None
        self._owned_sources: set[Path] = set()
        self._missing_media_deps_warned: set[str] = set()

        # On-disk run directories. _run_dir is the offline mode's run; _spill
        # is created lazily by the online mode the first time it would
        # otherwise drop points (or artifacts). _spill_lock serialises
        # creating it and writing to it. Lock order, all told:
        # _send_lock -> _spill_lock -> _lock, and _upload_lock -> _spill_lock
        # -> _lock; _upload_lock and _send_lock are never held together.
        self._run_dir: _offline.RunDir | None = None
        self._spill: _offline.RunDir | None = None
        self._spill_unavailable = False
        self._spill_lock = threading.Lock()
        self._spill_config_written = False
        self._spill_last_config: Any = None

        # System metrics (GPU/CPU/memory) piggyback on the flush timer
        # below rather than running a second background thread; see
        # _maybe_collect_system_metrics().
        self._system_metrics_enabled = not _system_metrics.is_disabled()
        self._last_system_metrics_at: float | None = None

        if repo is not None:
            self.namespace, self.repo_name = _split_repo(repo)
        elif not self.offline:
            raise ValueError("an online run needs a repository")
        else:
            # Resolved as "{user}/trackio-metrics" by `tf experiments sync`.
            self.namespace = self.repo_name = ""
        _check_path_segment("project", project)

        # One warning per run: an offline run that continues an existing one
        # cannot know the server's last_step, so auto-numbered steps restart
        # at 0 (see init()).
        self._offline_resume_step_warned = False
        # Set by finish()'s final artifact commit once it has taken the staged
        # list: anything staged after that would sit in a list nobody commits.
        self._artifacts_closed = False

        if self.offline:
            self._open_offline_dir()

        self._schedule_flush()

    # -- on-disk run directories ---------------------------------------------

    def _init_record(self, resume: str, spill: bool = False) -> dict[str, Any]:
        """The ``init`` record of a run directory.

        For a ``spill`` directory (the online mode's), ``config`` is null when
        the current config already reached the server: replaying it would
        only roll back a newer config the run may be given later, so the
        syncer sends a config only when a record carries one. Likewise
        ``repo`` is null when the repository was never resolved.
        """
        with self._lock:
            config, _ = _sanitize.sanitize(self.config)
            if spill and self._config_delivered_locked(config):
                config = None
        repo = self.repo if (self.repo_resolved or not spill) else None
        return {
            "type": "init",
            "time": _utc_now_iso(),
            "repo": repo,
            "project": self.project,
            "run": self.name,
            "resume": resume,
            "group": self.group,
            "job_type": self.job_type,
            "config": config,
        }

    def _open_offline_dir(self) -> None:
        """Create the offline run's directory and write its ``init`` record.

        A directory that cannot be created is a warning, not an exception --
        the run then records nothing, and says so once, here.
        """
        try:
            run_dir = _offline.RunDir.create(self.project, self.name)
            run_dir.append(self._init_record(self.resume_mode))
        except Exception as exc:  # noqa: BLE001 - never abort the training script
            warnings.warn(
                f"thinkingface.trackio: offline mode could not create a run directory under "
                f"{_offline.base_dir()} ({exc!r}); nothing from run {self.name!r} will be "
                "recorded. Set THINKINGFACE_OFFLINE_DIR to a writable directory."
            )
            return
        self._run_dir = run_dir
        print(
            f"thinkingface.trackio: offline mode -- run {self.name!r} is being written to "
            f"{run_dir.path}; {_sync_hint(run_dir.path)}.",
            file=sys.stderr,
        )

    def _ensure_spill_locked(self) -> _offline.RunDir | None:
        """The online run's spill directory, created on first use.

        The caller holds _spill_lock. The ``init`` record says
        ``resume: "allow"``: the directory continues a run the server already
        has, so syncing it must append to that run rather than rename.
        """
        if self._spill is not None:
            return self._spill
        if self._spill_unavailable:
            return None
        try:
            spill = _offline.RunDir.create(self.project, self.name)
            spill.append(self._init_record("allow", spill=True))
        except Exception:  # noqa: BLE001 - the caller falls back to dropping
            self._spill_unavailable = True
            return None
        self._spill = spill
        return spill

    def _spill_points(self, points: list[dict[str, Any]]) -> Path | None:
        """Write points the online mode is giving up on to the spill directory.

        Returns the directory on success and None when the disk could not
        take them, in which case the caller drops them with a warning as it
        always did. Never raises.
        """
        if not points:
            return None
        try:
            with self._lock:
                config, _ = _sanitize.sanitize(self.config)
                # Already on the server: leave it out, so replaying this
                # record cannot put an older config back (see _init_record).
                if self._config_delivered_locked(config):
                    config = None
        except Exception:  # noqa: BLE001
            config = None
        with self._spill_lock:
            spill = self._ensure_spill_locked()
            if spill is None:
                return None
            send_config = None
            if config is not None and (
                not self._spill_config_written or config != self._spill_last_config
            ):
                send_config = config
            try:
                _append_log_record(spill, points, send_config)
            except Exception:  # noqa: BLE001
                return None
            if send_config is not None:
                self._spill_config_written = True
                self._spill_last_config = send_config
            return spill.path

    def _config_delivered_locked(self, config: Any) -> bool:
        """Whether ``config`` (sanitised) is exactly what the server last
        accepted for this run. The caller holds _lock."""
        return self._config_sent and config == self._last_sent_config

    def _spill_artifacts(self, staged: list[tuple[Any, str]]) -> Path | None:
        """Copy artifacts that could not be committed into the spill directory."""
        with self._spill_lock:
            spill = self._ensure_spill_locked()
            if spill is None:
                return None
            try:
                for source, name in staged:
                    spill.add_artifact(Path(source), name)
            except Exception:  # noqa: BLE001
                return None
            return spill.path

    def _close_spill(self, status: str) -> None:
        with self._spill_lock:
            if self._spill is None:
                return
            try:
                self._spill.append({"type": "finish", "time": _utc_now_iso(), "status": status})
            except Exception as exc:  # noqa: BLE001
                warnings.warn(
                    f"thinkingface.trackio: could not close the spill directory "
                    f"{self._spill.path} ({exc!r}); syncing it will leave run "
                    f"{self.name!r} running."
                )

    # -- HTTP -------------------------------------------------------------

    @property
    def _base_url(self) -> str:
        return _project_url(self.endpoint, self.namespace, self.repo_name, self.project)

    @property
    def _log_url(self) -> str:
        return f"{self._base_url}/log"

    @property
    def _finish_url(self) -> str:
        return f"{self._base_url}/finish"

    def _headers(self) -> dict[str, str]:
        headers = {"Content-Type": "application/json"}
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        return headers

    # -- background flush timer -------------------------------------------

    def _schedule_flush(self) -> None:
        self._timer = threading.Timer(_FLUSH_INTERVAL_SECONDS, self._on_timer)
        self._timer.daemon = True
        self._timer.start()

    def _on_timer(self) -> None:
        # The read of _finished below races finish(), and is left that way on
        # purpose: locking here would mean holding _lock across flush()'s HTTP
        # call, which is the thing log() must never wait behind.
        #
        # The race arms one extra timer at worst. finish() can set the flag and
        # cancel the current timer in the window between this check and
        # _schedule_flush, leaving a timer running that finish() has already
        # said goodbye to. What that timer then does is nothing:
        # _maybe_collect_system_metrics returns on the flag, flush() finds the
        # buffer empty -- finish() drains it before setting the flag, and log()
        # refuses to add to it afterwards -- and this check, now reading True,
        # does not schedule another. One wakeup of a daemon thread, no request,
        # no duplicated point.
        self._maybe_collect_system_metrics()
        self.flush()
        try:
            self._maybe_heartbeat()
        except Exception:  # noqa: BLE001 - must never stop the timer
            pass
        if not self._finished:
            self._schedule_flush()

    # -- liveness ------------------------------------------------------------

    def _maybe_heartbeat(self) -> None:
        """Post a point-less batch if the run has been quiet for heartbeat_secs.

        The server derives "stale" from how long a running run has gone
        without an update (docs/dev/agent-features.md §2.6), so a training
        step that takes ten minutes must not read as a dead process. A ping is
        ``{"run", "status": "running", "points": []}`` plus the heartbeat and
        grouping fields -- no config, which stays with the real batches.

        Under _send_lock like every other request, so it can never land after
        finish()'s /finish; and never once finish() has started. A failed
        ping is silent (the next real flush says everything worth saying) and
        is not retried until another heartbeat_secs has passed, so an
        unreachable server is not polled every tick.
        """
        if self.offline or self.heartbeat_secs <= 0 or self._finishing or self._finished:
            return
        now = time.monotonic()
        if (
            now - self._last_post_at < self.heartbeat_secs
            or now - self._last_heartbeat_attempt_at < self.heartbeat_secs
        ):
            return
        with self._send_lock:
            if self._finishing or self._finished:
                return
            now = time.monotonic()
            if now - self._last_post_at < self.heartbeat_secs:
                return  # a flush got there first
            self._last_heartbeat_attempt_at = now
            payload: dict[str, Any] = {
                "run": self.name,
                "status": "running",
                "points": [],
                "heartbeat_secs": self.heartbeat_secs,
            }
            if self.group:
                payload["group"] = self.group
            if self.job_type:
                payload["job_type"] = self.job_type
            try:
                resp = requests.post(
                    self._log_url,
                    json=payload,
                    headers=self._headers(),
                    timeout=_REQUEST_TIMEOUT_SECONDS,
                )
            except Exception:  # noqa: BLE001 - a ping must never raise
                return
            if getattr(resp, "ok", False):
                self._last_post_at = time.monotonic()

    # -- background system-metrics sampling --------------------------------

    def _maybe_collect_system_metrics(self) -> None:
        """Sample GPU/CPU/memory telemetry, throttled to roughly once per
        ``_system_metrics.DEFAULT_INTERVAL_SECONDS``.

        Called from the flush-timer thread on every tick (every
        ``_FLUSH_INTERVAL_SECONDS``, 5s by default) rather than from a
        second timer of its own, so the 10s system-metrics cadence is
        approximated by skipping every other tick.
        """
        if not self._system_metrics_enabled or self._finished or self._finishing:
            return
        now = time.monotonic()
        if (
            self._last_system_metrics_at is not None
            and now - self._last_system_metrics_at < _system_metrics.DEFAULT_INTERVAL_SECONDS
        ):
            return
        self._last_system_metrics_at = now
        try:
            metrics = _system_metrics.collect()
        except Exception:  # collection must never break the flush loop
            return
        if metrics:
            self._log_system_metrics(metrics)

    def _log_system_metrics(self, metrics: dict[str, float]) -> None:
        """Append a system-telemetry point at the *current* step, without
        advancing it -- unlike log(), so background sampling never shifts
        the step numbering of the run's own metrics."""
        sendable, _ = _finite_metrics(metrics)
        if not sendable:
            return
        with self._lock:
            # _finishing too: finish() has drained (or is draining) the buffer
            # for the last time, so a point added now would never be sent --
            # or, offline, would land after the finish record.
            if self._finished or self._finishing:
                return
            self._buffer.append(
                {
                    "step": self.step,
                    "timestamp": _utc_now_iso(),
                    "metrics": sendable,
                }
            )
            # Counted towards the server's per-run key ceiling like any other
            # name, but never warned about: this set is fixed and small, and
            # the caller did not choose it.
            self._metric_keys.update(sendable)

    # -- public API ---------------------------------------------------------

    def log(self, metrics: dict[str, Any], step: int | None = None) -> None:
        if self._finished or self._finishing:
            self._warn_log_after_finish()
            return
        media, metrics = _split_media(metrics)
        sendable, nonfinite = _finite_metrics(metrics)
        with self._lock:
            # Checked again under _lock, which finish() sets _finishing under:
            # once finish() has started, its final drain may already have run,
            # so a point added now would never be sent (or could post
            # "running" after /finish, or -- offline -- land after the finish
            # record). It is refused with a warning rather than silently
            # lost, and so is its media.
            refused = self._finished or self._finishing
            if not refused:
                auto_step = step is None
                if step is None:
                    step = self.step
                self.step = max(self.step, step) + 1
                # A point whose every value was non-finite carries nothing; the
                # step still advances, so the next log() lands where it would
                # have. Likewise a point that held only media: those values
                # become artifacts, and an empty point would only be noise.
                if sendable or not (nonfinite or media):
                    self._buffer.append(
                        {
                            "step": step,
                            "timestamp": _utc_now_iso(),
                            "metrics": sendable,
                        }
                    )
                should_flush = len(self._buffer) >= _FLUSH_MAX_POINTS
                over_key_limit = self._note_metric_keys(sendable)
                warn_nonfinite = bool(nonfinite) and not self._nonfinite_warned
                if warn_nonfinite:
                    self._nonfinite_warned = True
                # Offline, a continued run cannot learn the server's last_step,
                # so auto-numbered steps start where init() put them (0).
                warn_offline_resume = (
                    auto_step
                    and self.offline
                    and self.resume_mode != "never"
                    and not self._offline_resume_step_warned
                )
                if warn_offline_resume:
                    self._offline_resume_step_warned = True
        if refused:
            self._warn_log_after_finish()
            return
        if warn_offline_resume:
            warnings.warn(
                f"thinkingface.trackio: offline run {self.name!r} was started with "
                f'resume="{self.resume_mode}", but its steps are numbered from {step} '
                "without regard to the server: an offline run cannot know the existing "
                "run's last_step, and `tf experiments sync` does not shift the steps. "
                "Pass step= explicitly to log() if this run continues an existing one."
            )
        if warn_nonfinite:
            warnings.warn(
                f"thinkingface.trackio: dropping non-finite value(s) for "
                f"{', '.join(repr(name) for name in nonfinite)} in run {self.name!r}: "
                "NaN and infinity cannot be represented in JSON, so a point carrying "
                "one can never be sent. Whatever else the point held was logged as "
                "usual, and further occurrences in this run are dropped without "
                "another warning."
            )
        if over_key_limit:
            warnings.warn(
                f"thinkingface.trackio: run {self.name!r} has logged more than "
                f"{_MAX_METRIC_KEYS} distinct metric names, which is the most the "
                "server accepts; further batches for this run will be rejected with "
                "400. Metric names are meant to be a fixed set -- if one is built "
                "from a step number, an id or a filename, move that part into the "
                "value or the run name."
            )
        if media:
            self._stage_media(media, step)
        if should_flush:
            self.flush()

    @staticmethod
    def _warn_log_after_finish() -> None:
        warnings.warn("thinkingface.trackio: log() called after finish(); ignoring.")

    def _note_metric_keys(self, metrics: Any) -> bool:
        """Record the batch's metric names; True the first time the run goes
        over the server's ceiling. The caller holds _lock and warns outside it.
        """
        try:
            self._metric_keys.update(metrics)
        except TypeError:  # not a mapping; the server will say so
            return False
        if self._key_limit_warned or len(self._metric_keys) <= _MAX_METRIC_KEYS:
            return False
        self._key_limit_warned = True
        return True

    def flush(self) -> None:
        # _send_lock serializes this against any other flush() (the timer
        # thread's and finish()'s own) and against finish()'s POST /finish, so
        # only one HTTP request for this run is ever in flight at a time. That
        # is what stops a slow /log POST the timer already started from
        # landing at the server *after* finish()'s /finish -- which would
        # flip a finished run's status back to "running", since the ingest
        # upsert applies "status": "running" from every /log unconditionally.
        #
        # It is taken *before* the buffer is drained, not just around the
        # send. Draining first left a window where the buffer was empty while
        # its points were still on the wire: finish() saw nothing to flush,
        # and if that send then failed, _requeue() put the points back after
        # finish() had already given up on them. Holding _send_lock across
        # the drain means an empty buffer really does mean "nothing unsent".
        # Lock order is always _send_lock -> _lock.
        with self._send_lock:
            with self._lock:
                if not self._buffer:
                    return
                # Settle the config before the buffer is drained:
                # _config_to_send reads values the caller owns, and anything
                # raised after the drain would strand points that are no longer
                # in _buffer and not yet requeued.
                config = self._config_to_send()
                points, self._buffer = self._buffer, []
            if self.offline:
                self._write_offline(points, config)
            else:
                self._send(points, config)

    def _write_offline(self, points: list[dict[str, Any]], config: dict[str, Any] | None) -> None:
        """The offline mode's flush: one ``log`` record in run.jsonl.

        The caller holds _send_lock, which is what keeps records in step
        order across the timer thread and an inline flush from log().
        """
        if self._run_dir is None:
            return  # init() already warned that nothing is being recorded
        try:
            left_out = _append_log_record(self._run_dir, points, config)
        except Exception as exc:  # noqa: BLE001 - never raise into training
            warnings.warn(
                f"thinkingface.trackio: dropping {len(points)} point(s) for run "
                f"{self.name!r}: could not write to {self._run_dir.run_file} ({exc!r})."
            )
            return
        if left_out:
            warnings.warn(
                f"thinkingface.trackio: dropping {left_out} point(s) for run {self.name!r}: "
                "they cannot be encoded as JSON."
            )
        if config is not None:
            with self._lock:
                self._config_sent = True
                self._last_sent_config = config

    def _send(self, points: list[dict[str, Any]], config: dict[str, Any] | None) -> None:
        """POST ``points`` in batches the server will accept.

        The buffer can legitimately hold more than one request's worth: after a
        disconnection _requeue() puts back up to _BUFFER_MAX_POINTS and log()
        keeps adding on top of that. Sent as a single body, one point past
        maxIngestPoints earned a 400 -- which is not retryable, so the whole
        buffer was thrown away for being one point too long. Splitting here
        keeps every request inside the server's limit, leaving only the 400s
        that really are about the payload.
        """
        for start in range(0, len(points), _MAX_POINTS_PER_REQUEST):
            chunk = points[start : start + _MAX_POINTS_PER_REQUEST]
            outcome = self._post_points(chunk, config)
            if outcome == "sent":
                # Accepted, so later chunks need not repeat it.
                config = None
                continue
            if outcome == "retry":
                # This chunk and everything after it is still unsent.
                self._requeue(points[start:])
                return
            # "drop": the request itself was refused (bad token, unknown
            # repo, ...), which the remaining chunks would hit identically.
            abandoned = len(points) - start - len(chunk)
            if abandoned > 0:
                warnings.warn(
                    f"thinkingface.trackio: also dropping the remaining {abandoned} "
                    f"buffered point(s) for run {self.name!r}."
                )
            return

    def _post_points(self, points: list[dict[str, Any]], config: dict[str, Any] | None) -> str:
        """Send one batch. Returns "sent", "retry" (requeue) or "drop"."""
        payload: dict[str, Any] = {
            "run": self.name,
            "status": "running",
            "points": points,
        }
        if config is not None:
            payload["config"] = config
        if self.heartbeat_secs > 0:
            payload["heartbeat_secs"] = self.heartbeat_secs
        if self.group:
            payload["group"] = self.group
        if self.job_type:
            payload["job_type"] = self.job_type

        try:
            resp = requests.post(
                self._log_url,
                json=payload,
                headers=self._headers(),
                timeout=_REQUEST_TIMEOUT_SECONDS,
            )
        except _ENCODE_ERRORS as exc:
            # Nothing was sent and nothing ever will be: this batch is not a
            # network fault and must never be requeued, or every flush from
            # here on re-raises it and the run stops delivering metrics.
            if config is not None:
                # Either half of the body could be the unencodable one: the
                # config is the likelier culprit (log() strips NaN/inf from
                # metrics), but not the only one -- a numpy scalar is finite,
                # so _finite_metrics passes it through and json chokes on the
                # points instead. Retry with the points alone and let *that*
                # call decide which half was at fault; the points are the part
                # that cannot be reconstructed, so they go first either way.
                outcome = self._post_points(points, None)
                if outcome != "sent":
                    # The points were the bad half (or the send failed for an
                    # unrelated reason). The config was never transmitted, so
                    # it stays pending and goes out with the next batch --
                    # marking it delivered here would silence it forever.
                    return outcome
                warnings.warn(
                    f"thinkingface.trackio: the config for run {self.name!r} cannot be "
                    f"encoded as JSON ({exc!r}); the points were sent without it."
                )
                with self._lock:
                    # Only now, on a call that really did reach the server:
                    # treated as delivered so it is not re-encoded on every
                    # flush; a config that is later changed compares unequal
                    # again and is retried then.
                    self._config_sent = True
                    self._last_sent_config = config
                return outcome
            warnings.warn(
                f"thinkingface.trackio: dropping {len(points)} point(s) for run "
                f"{self.name!r}: the batch cannot be encoded as JSON ({exc!r})."
            )
            return "drop"
        except Exception as exc:  # network failures must never raise
            warnings.warn(
                f"thinkingface.trackio: failed to send {len(points)} point(s) "
                f"for run {self.name!r} ({exc!r}); will retry on next flush."
            )
            return "retry"

        if resp.ok:
            self._last_post_at = time.monotonic()
            with self._lock:
                self._config_sent = True
                if config is not None:
                    self._last_sent_config = config
            return "sent"

        # A 4xx (bad token, unknown repo, malformed payload) will not fix itself
        # by being retried: keeping the points would grow the buffer forever and
        # re-send a rejected body every 5 seconds. 408/429 and every 5xx are
        # transient, so those are requeued.
        retryable = resp.status_code >= 500 or resp.status_code in (408, 429)
        detail = resp.text[:200].strip()
        if retryable:
            warnings.warn(
                f"thinkingface.trackio: server returned {resp.status_code} for run "
                f"{self.name!r} ({detail!r}); will retry on next flush."
            )
            return "retry"
        warnings.warn(
            f"thinkingface.trackio: dropping {len(points)} point(s) for run "
            f"{self.name!r}: server returned {resp.status_code} ({detail!r}). "
            "Check THINKINGFACE_TOKEN / THINKINGFACE_REPO."
        )
        return "drop"

    def _config_to_send(self) -> dict[str, Any] | None:
        """The config for the next flush, or None to leave it out.

        Resent whenever it differs from what was last accepted -- not just on
        the very first flush -- so a config mutated afterwards (`run.config`
        updated in place, or a hyperparameter logged once training has started)
        still reaches the server instead of being dropped forever.

        What is sent is a JSON-safe copy made by ``_sanitize.sanitize``: a
        ``Path`` becomes its string, an ``Enum`` its value, a dataclass or an
        ``argparse.Namespace`` its fields, a numpy scalar its Python number,
        NaN the string ``"nan"`` -- and anything unrecognised ``str(value)``,
        which is warned about once per run with the keys it affected. Comparing
        sanitised copies (rather than the caller's objects) is also what makes
        "unchanged" well defined for values without a useful ``__eq__``.

        The caller holds _lock. Sanitising runs over values the caller put in
        config and so could still raise in some exotic case; that is not worth
        failing a flush over -- the points are the part that cannot be
        reconstructed, and an exception out of flush() on the timer thread
        would also stop _schedule_flush from ever running again -- so a config
        that cannot be prepared is reported and skipped.
        """
        try:
            config, stringified = _sanitize.sanitize(self.config)
            if self._config_sent and config == self._last_sent_config:
                return None
        except Exception as exc:  # noqa: BLE001 - metrics matter more
            warnings.warn(
                f"thinkingface.trackio: could not prepare the config for run "
                f"{self.name!r} ({exc!r}); sending metrics without it."
            )
            return None
        if stringified and not self._config_fallback_warned:
            self._config_fallback_warned = True
            shown = ", ".join(repr(key) for key in stringified[:10])
            more = f" and {len(stringified) - 10} more" if len(stringified) > 10 else ""
            warnings.warn(
                f"thinkingface.trackio: config value(s) {shown}{more} of run {self.name!r} "
                "are not JSON types and were recorded as their str(); convert them "
                "yourself if that is not what you want to see on the run page."
            )
        return config

    def _requeue(self, points: list[dict[str, Any]]) -> None:
        """Put unsent points back at the front, capped at _BUFFER_MAX_POINTS."""
        evicted: list[dict[str, Any]] = []
        with self._lock:
            self._buffer = points + self._buffer
            overflow = len(self._buffer) - _BUFFER_MAX_POINTS
            if overflow > 0:
                # Evict the oldest: the recent tail of a training curve is the
                # part still worth delivering live.
                evicted = self._buffer[:overflow]
                del self._buffer[:overflow]
        if overflow > 0:
            # Out of memory is not out of disk: the evicted points go to the
            # spill directory, which `tf experiments sync` delivers later.
            where = self._spill_points(evicted)
            if where is not None:
                warnings.warn(
                    f"thinkingface.trackio: retry buffer full; wrote the {overflow} oldest "
                    f"point(s) for run {self.name!r} to {where} instead -- "
                    f"{_sync_hint(where)} once the server is reachable."
                )
            else:
                warnings.warn(
                    f"thinkingface.trackio: retry buffer full, dropped {overflow} "
                    f"oldest point(s) for run {self.name!r}."
                )

    # -- artifacts and produced models -------------------------------------

    def log_artifact(self, path: Any, name: str | None = None) -> None:
        """Stage a file (or a whole directory) for the next artifact commit.

        Offline, the files are copied into the run directory right away (the
        commit happens at sync time); online they are committed by the
        background uploader, save() or finish(), whichever comes first.
        """
        if self._finished or self._finishing:
            warnings.warn("thinkingface.trackio: log_artifact() called after finish(); ignoring.")
            return
        try:
            staged = _artifacts.stage(path, name)
        except (ValueError, OSError) as exc:
            # A bad name or an unreadable path is the caller's mistake, but
            # this shim never aborts a training script over bookkeeping. The
            # wording is emphatic on purpose: "ignored" alone reads like a
            # partial upload, and the common cause -- a checkpoint directory
            # over the per-call file limit -- uploads nothing at all.
            warnings.warn(f"thinkingface.trackio: log_artifact({path!r}) uploaded nothing: {exc}")
            return
        if self.offline:
            if self._run_dir is None:
                return
            try:
                for source, artifact_name in staged:
                    self._run_dir.add_artifact(source, artifact_name)
            except Exception as exc:  # noqa: BLE001 - never raise into training
                warnings.warn(
                    f"thinkingface.trackio: log_artifact({path!r}) could not be copied into "
                    f"{self._run_dir.path} ({exc!r})."
                )
            return
        with self._lock:
            closed = self._artifacts_closed
            if not closed:
                self._artifacts.extend(staged)
        if closed:
            warnings.warn("thinkingface.trackio: log_artifact() called after finish(); ignoring.")
            return
        self._ensure_uploader()

    def _media_staging_dir(self) -> Path:
        with self._lock:
            if self._media_dir is None:
                self._media_dir = Path(tempfile.mkdtemp(prefix="thinkingface-media-"))
            return self._media_dir

    def _stage_media(self, media: dict[str, Any], step: Any) -> None:
        """Write each Image / Table value to a file and stage it as an artifact
        named ``media/{key}/step_{step:08d}.png`` / ``tables/{key}/step_{step:08d}.parquet``.
        Every failure is a warning; a missing optional library is warned about
        once per run."""
        for key, value in media.items():
            directory, extension = _media.kind(value) or ("media", "bin")
            try:
                name = _artifacts.normalize_artifact_name(
                    f"{directory}/{key}/step_{int(step):08d}.{extension}"
                )
            except (TypeError, ValueError) as exc:
                warnings.warn(f"thinkingface.trackio: not logging {key!r} at step {step!r}: {exc}")
                continue
            try:
                if self.offline:
                    if self._run_dir is None:
                        continue
                    target = self._run_dir.artifact_target(name)
                else:
                    target = self._media_staging_dir() / name
                    target.parent.mkdir(parents=True, exist_ok=True)
                written = _media.write(value, target)
                if written != target:
                    name = name[: -len(target.suffix)] + written.suffix
                if self.offline:
                    self._run_dir.record_artifact(name)
                    continue
                with self._lock:
                    closed = self._artifacts_closed
                    if not closed:
                        self._artifacts.append((written, name))
                        self._owned_sources.add(written)
                if closed:
                    # finish() committed the last batch while this was being
                    # written; nothing would ever commit it now.
                    written.unlink(missing_ok=True)
                    warnings.warn(
                        f"thinkingface.trackio: not logging {key!r} at step {step}: "
                        "the run finished while it was being written."
                    )
                    continue
            except _media.MediaError as exc:
                dependency = exc.missing_dependency
                if dependency is not None:
                    if dependency in self._missing_media_deps_warned:
                        continue
                    self._missing_media_deps_warned.add(dependency)
                warnings.warn(f"thinkingface.trackio: not logging {key!r} at step {step}: {exc}")
                continue
            except Exception as exc:  # noqa: BLE001 - never raise into training
                warnings.warn(
                    f"thinkingface.trackio: not logging {key!r} at step {step}: "
                    f"could not write it ({exc!r})."
                )
                continue
        if not self.offline:
            self._ensure_uploader()

    def _ensure_uploader(self) -> None:
        """Start the background artifact committer on first need.

        A thread of its own rather than the flush timer's: a commit of a large
        file can take minutes, and the flush timer is what delivers metrics
        and heartbeats -- neither may wait behind an upload.
        """
        if self._artifact_interval <= 0 or self._upload_thread is not None:
            return
        with self._lock:
            if self._upload_thread is not None or self._finishing or self._finished:
                return
            thread = threading.Thread(
                target=self._upload_loop, name="thinkingface-artifacts", daemon=True
            )
            self._upload_thread = thread
        thread.start()

    def _upload_loop(self) -> None:
        while not self._upload_stop.wait(self._artifact_interval):
            if self._finishing or self._finished:
                return
            try:
                self._upload_artifacts(final=False)
            except Exception:  # noqa: BLE001 - the loop must survive anything
                pass

    def save(self) -> None:
        """Commit every pending artifact now instead of at the next interval.

        Offline there is nothing to commit (files are copied as they are
        logged), so this writes out the buffered points instead.
        """
        if self._finished or self._finishing:
            return
        if self.offline:
            self.flush()
            return
        self._upload_artifacts(final=False)

    def log_model(self, repo_id: str, revision: str | None = None) -> None:
        """Record that this run produced ``repo_id`` at ``revision``."""
        if self._finished or self._finishing:
            warnings.warn("thinkingface.trackio: log_model() called after finish(); ignoring.")
            return
        try:
            namespace, model_name = _split_repo(repo_id)
        except ValueError as exc:
            warnings.warn(f"thinkingface.trackio: log_model({repo_id!r}) ignored: {exc}")
            return
        if self.offline:
            # No network: the revision is recorded as given, and "" means
            # "whatever HEAD is" to the syncer, which cannot know better.
            if self._run_dir is None:
                return
            try:
                self._run_dir.append(
                    {
                        "type": "model",
                        "repo_id": f"{namespace}/{model_name}",
                        "revision": revision or "",
                    }
                )
            except Exception as exc:  # noqa: BLE001
                warnings.warn(
                    f"thinkingface.trackio: log_model({repo_id!r}) could not be recorded "
                    f"in {self._run_dir.run_file} ({exc!r})."
                )
            return
        resolved = revision if revision is not None else self._resolve_model_head(repo_id)
        with self._lock:
            self._models.append(
                {"repo_id": f"{namespace}/{model_name}", "revision": resolved or ""}
            )
            self._models_dirty = True

    def _resolve_model_head(self, repo_id: str) -> str:
        """HEAD of the model repository's default branch, or "" if unknown.

        Called when ``log_model`` is given no revision, which is the common
        case: a training job pushes the model and then says "that one", so the
        revision worth recording is whatever the push just produced. The
        record is kept either way -- an unresolvable revision is stored empty
        and the run page links to the repository instead.
        """
        try:
            resp = requests.get(
                f"{self.endpoint}/api/models/{quote(repo_id, safe='/')}",
                headers=self._headers(),
                timeout=_REQUEST_TIMEOUT_SECONDS,
            )
            resp.raise_for_status()
            return str(resp.json().get("sha") or "")
        except Exception as exc:
            warnings.warn(
                f"thinkingface.trackio: could not resolve the current revision of "
                f"{repo_id!r} ({exc!r}); recording the model without one."
            )
            return ""

    def _upload_artifacts(self, final: bool = True) -> None:
        """Commit every staged artifact in one go.

        Uploading goes through ``huggingface_hub``, i.e. through the same
        preupload/commit endpoints any other client uses: large files are
        routed to LFS by the repository's ``.gitattributes``, and the result
        is ordinary git content rather than an opaque blob store.

        _upload_lock makes this the only commit in progress for the run. A
        failed background commit (``final=False``) puts the files back for
        the next attempt; finish()'s (``final=True``) is the last one, so its
        files go to the spill directory instead -- or, failing that, are
        reported as not committed.
        """
        with self._upload_lock:
            with self._lock:
                staged, self._artifacts = self._artifacts, []
                if final:
                    # The last commit: media a log() already in progress
                    # stages from now on is refused instead of left behind.
                    self._artifacts_closed = True
            if not staged:
                return
            # The same name staged twice (a media key logged twice at one
            # step, a file re-logged) is one file in the commit: the last.
            latest: dict[str, tuple[Any, str]] = {}
            for source, name in staged:
                latest.pop(name, None)
                latest[name] = (source, name)
            staged = list(latest.values())
            try:
                from huggingface_hub import CommitOperationAdd, HfApi

                operations = [
                    CommitOperationAdd(
                        path_in_repo=_artifacts.artifact_path(self.project, self.name, name),
                        path_or_fileobj=str(source),
                    )
                    for source, name in staged
                ]
                HfApi(endpoint=self.endpoint, token=self.token).create_commit(
                    repo_id=self.repo,
                    repo_type="dataset",
                    operations=operations,
                    commit_message=f"chore(trackio): artifacts for {self.project}/{self.name}",
                )
            except Exception as exc:  # uploads must never abort a training script
                if not final:
                    with self._lock:
                        self._artifacts = staged + self._artifacts
                    warnings.warn(
                        f"thinkingface.trackio: failed to upload {len(staged)} artifact(s) for "
                        f"run {self.name!r} ({exc!r}); will retry."
                    )
                    return
                where = self._spill_artifacts(staged)
                if where is not None:
                    warnings.warn(
                        f"thinkingface.trackio: failed to upload {len(staged)} artifact(s) for "
                        f"run {self.name!r} ({exc!r}); copies were written to {where} -- "
                        f"{_sync_hint(where)}."
                    )
                else:
                    warnings.warn(
                        f"thinkingface.trackio: failed to upload {len(staged)} artifact(s) for "
                        f"run {self.name!r} ({exc!r}); they were not committed."
                    )
                return
            self._release_owned(staged)

    def _release_owned(self, staged: list[tuple[Any, str]]) -> None:
        """Delete committed media files this run wrote itself (never the
        caller's own files from log_artifact)."""
        for source, _ in staged:
            path = Path(source)
            with self._lock:
                # Staged again since (the same media key logged at the same
                # step once more): the pending copy still needs the file.
                still_pending = any(Path(pending) == path for pending, _ in self._artifacts)
                owned = path in self._owned_sources and not still_pending
                if owned:
                    self._owned_sources.discard(path)
            if owned:
                try:
                    path.unlink()
                except OSError:
                    pass

    def _sync_models(self) -> None:
        """Write the produced-model list onto the run.

        It rides the annotation endpoint (PATCH .../runs/{run}) rather than
        the ingest payload on purpose: annotations are the fields the parquet
        indexer never touches, so a re-index of the project cannot erase what
        the training script declared it built.
        """
        if not self._models_dirty:
            return
        try:
            resp = requests.patch(
                f"{self._base_url}/runs/{quote(self.name, safe='')}",
                json={"models": self._models},
                headers=self._headers(),
                timeout=_REQUEST_TIMEOUT_SECONDS,
            )
            resp.raise_for_status()
        except Exception as exc:  # network failures must never raise
            warnings.warn(
                f"thinkingface.trackio: failed to record {len(self._models)} produced "
                f"model(s) for run {self.name!r} ({exc!r})."
            )

    def _has_buffered_points(self) -> bool:
        with self._lock:
            return bool(self._buffer)

    def _give_up_and_drop_buffer(self) -> list[dict[str, Any]]:
        """Clear the buffer and return the points that were left in it.

        Called only once finish()'s retry budget (_drain_for_finish) is
        exhausted. Clearing rather than leaving the points in place keeps the
        warning below honest: without this, a stray timer tick racing in
        after finish() has already given up (see the module-level race note
        on _on_timer) could still find them and send them late, making
        "dropped" a lie for whichever points that tick happened to catch.
        """
        with self._lock:
            dropped, self._buffer = self._buffer, []
        return dropped

    def _drain_for_finish(self) -> None:
        """Flush the buffer for the last time, retrying if it does not empty.

        flush() never raises and never tells its caller "sent" from "queued
        for later": a retryable failure (5xx / network) requeues the points
        and warns "will retry on next flush" -- true only while a timer is
        still scheduled to call it again. finish() is about to cancel that
        timer for good, so a single flush() call here is not enough: without
        retrying, a transient failure at exactly this moment would silently
        strand the run's final batch in memory, already having told the user
        (via that same warning) that it would be retried.

        Bounded to a handful of attempts a little further apart each time
        (_FINISH_FLUSH_ATTEMPTS), not an unbounded loop: finish() is a call a
        training script is blocked on and must still return. What is still
        unsent after that goes to the spill directory for `tf experiments
        sync`, and is only dropped if the disk refuses it too.
        """
        self.flush()
        attempt = 1
        backoff = _FINISH_FLUSH_BACKOFF_SECONDS
        while self._has_buffered_points() and attempt < _FINISH_FLUSH_ATTEMPTS:
            time.sleep(backoff)
            backoff = min(backoff * 2, _FINISH_FLUSH_MAX_BACKOFF_SECONDS)
            self.flush()
            attempt += 1
        if self._has_buffered_points():
            points = self._give_up_and_drop_buffer()
            where = self._spill_points(points)
            if where is not None:
                warnings.warn(
                    f"thinkingface.trackio: giving up after {attempt} attempt(s) to "
                    f"flush run {self.name!r}: {len(points)} point(s) were never "
                    f"delivered to the server; they were written to {where} instead -- "
                    f"{_sync_hint(where)} once the server is reachable."
                )
            else:
                warnings.warn(
                    f"thinkingface.trackio: giving up after {attempt} attempt(s) to "
                    f"flush run {self.name!r}: {len(points)} point(s) were never "
                    "delivered to the server and are being dropped now that the run "
                    "is finishing."
                )

    def _stop_background(self) -> None:
        self._finished = True
        if self._timer is not None:
            self._timer.cancel()
        self._upload_stop.set()

    def _cleanup_media_dir(self) -> None:
        with self._lock:
            media_dir, self._media_dir = self._media_dir, None
            self._owned_sources.clear()
        if media_dir is not None:
            shutil.rmtree(media_dir, ignore_errors=True)

    def finish(self, status: str = "finished") -> None:
        # Under _lock, like log()'s own check: a log() that got into the
        # buffer before this has its point drained below, and one that comes
        # after is refused -- none falls in between.
        with self._lock:
            if self._finished or self._finishing:
                return
            self._finishing = True
        if self.offline:
            self._finish_offline(status)
            return
        self._drain_for_finish()
        self._stop_background()
        self._upload_artifacts(final=True)
        self._cleanup_media_dir()
        finish_payload: dict[str, Any] = {"run": self.name, "status": status}
        # Also on finish: a run that logged no points at all is created by
        # this call, so without it such a run would fall out of its sweep.
        if self.group:
            finish_payload["group"] = self.group
        if self.job_type:
            finish_payload["job_type"] = self.job_type
        # _send_lock: see flush() for why /finish must never be in flight at
        # the same time as a /log POST for this run -- it waits here for a
        # flush _drain_for_finish already triggered (or one the timer started
        # independently) to land before this run's status is set for the
        # last time.
        with self._send_lock:
            try:
                resp = requests.post(
                    self._finish_url,
                    json=finish_payload,
                    headers=self._headers(),
                    timeout=_REQUEST_TIMEOUT_SECONDS,
                )
                resp.raise_for_status()
            except Exception as exc:  # network failures must never raise
                warnings.warn(
                    f"thinkingface.trackio: failed to mark run {self.name!r} as {status!r} ({exc!r})."
                )
        # After the finish call: that is what guarantees the run row exists,
        # since a run that logged no points at all is created there.
        self._sync_models()
        # A spill directory, if this run needed one, ends the way the run did,
        # so syncing it leaves the server's status where finish() put it.
        self._close_spill(status)

    def _finish_offline(self, status: str) -> None:
        self.flush()
        self._stop_background()
        if self._run_dir is None:
            return
        try:
            self._run_dir.append({"type": "finish", "time": _utc_now_iso(), "status": status})
        except Exception as exc:  # noqa: BLE001
            warnings.warn(
                f"thinkingface.trackio: could not record the end of run {self.name!r} in "
                f"{self._run_dir.run_file} ({exc!r}); syncing it will leave the run running."
            )


def _split_media(metrics: Any) -> tuple[dict[str, Any], Any]:
    """Separate ``trackio.Image`` / ``trackio.Table`` values from the metrics."""
    if not isinstance(metrics, Mapping):
        return {}, metrics
    media = {key: value for key, value in metrics.items() if _media.kind(value) is not None}
    if not media:
        return {}, metrics
    return media, {key: value for key, value in metrics.items() if key not in media}


_current_run: _Run | None = None

# What an online run logs to when THINKINGFACE_REPO is unset and the current
# user could not be looked up. It is a placeholder, not a choice: a spill
# directory of such a run records `"repo": null` instead (see _Run.repo_resolved).
_FALLBACK_REPO = "unknown/trackio-metrics"


def _resolve_default_repo(endpoint: str, token: str | None) -> str | None:
    """Default THINKINGFACE_REPO: "{user}/trackio-metrics", or None (with a
    warning) when the current user cannot be looked up."""
    try:
        headers = {"Authorization": f"Bearer {token}"} if token else {}
        resp = requests.get(
            f"{endpoint}/api/v1/me", headers=headers, timeout=_REQUEST_TIMEOUT_SECONDS
        )
        resp.raise_for_status()
        username = resp.json()["user"]["username"]
        return f"{username}/trackio-metrics"
    except Exception as exc:
        warnings.warn(
            "thinkingface.trackio: could not resolve the current user to "
            f"build the default repo ({exc!r}); set THINKINGFACE_REPO "
            "explicitly, or the run will fail to log."
        )
        return None


# ---------------------------------------------------------------- resuming

# The three resume modes, spelled as wandb/trackio spell them:
#   "allow"  continue the run if it exists, otherwise start it
#   "must"   continue the run, and fail if it does not exist
#   "never"  (default) never continue: a name that is taken is given a
#            "-1", "-2", ... suffix instead
_RESUME_MODES = ("allow", "must", "never")

# Config keys this shim owns. They are replaced wholesale on a resume rather
# than diffed: _meta describes the *current* attempt's environment (a new
# commit, a new host), and _resume is the bookkeeping written below.
_RESERVED_CONFIG_KEYS = ("_meta", "_resume")


def _normalize_resume(resume: Any) -> str:
    """Map the accepted spellings of ``resume=`` onto one of _RESUME_MODES."""
    if resume is None or resume is False:
        return "never"
    if resume is True:  # wandb spells "continue if you can" as resume=True
        return "allow"
    mode = str(resume).strip().lower()
    if mode not in _RESUME_MODES:
        raise ValueError(
            f'resume must be one of "allow", "must", "never" (or True/False), got {resume!r}'
        )
    return mode


def _fetch_run(
    endpoint: str, token: str | None, repo: str, project: str, name: str
) -> tuple[dict[str, Any] | None, set[str]]:
    """Look one run up, and report which names the project already uses.

    Raises on a transport or HTTP failure; the caller decides whether not
    knowing is fatal (``resume="must"``) or merely a warning.
    """
    namespace, repo_name = _split_repo(repo)
    _check_path_segment("project", project)
    headers = {"Authorization": f"Bearer {token}"} if token else {}
    resp = requests.get(
        f"{_project_url(endpoint, namespace, repo_name, project)}/runs",
        headers=headers,
        timeout=_REQUEST_TIMEOUT_SECONDS,
    )
    resp.raise_for_status()
    runs = resp.json().get("runs") or []
    taken = {r.get("name") for r in runs if isinstance(r, dict)}
    for run in runs:
        if isinstance(run, dict) and run.get("name") == name:
            return run, taken
    return None, taken


def _unique_run_name(name: str, taken: set[str]) -> str:
    """First of ``name``, ``name-1``, ``name-2``, ... that is not in use."""
    if name not in taken:
        return name
    suffix = 1
    while f"{name}-{suffix}" in taken:
        suffix += 1
    return f"{name}-{suffix}"


def _merge_resumed_config(
    previous: dict[str, Any] | None, current: dict[str, Any], from_step: int
) -> dict[str, Any]:
    """Merge the previous attempt's config with this one's.

    The new value wins on a conflict -- the code that is running now is the
    truth about what it is running -- but the fact that something changed is
    not thrown away: it is recorded under ``_resume.config_changes`` so a run
    whose learning rate silently differs between attempts is still explicable
    from the run page alone.
    """
    merged = dict(previous or {})
    changes: dict[str, Any] = {}
    for key, value in current.items():
        # Compared as it will be stored: the previous config came back from
        # the server already sanitised, so a Path("out") must equal "out".
        if key not in _RESERVED_CONFIG_KEYS and key in merged:
            try:
                changed = merged[key] != _sanitize.sanitize(value)[0]
            except Exception:  # noqa: BLE001
                changed = merged[key] != value
            if changed:
                changes[key] = {"from": merged[key], "to": value}
        merged[key] = value

    history = merged.get("_resume")
    count = 0
    if isinstance(history, dict):
        try:
            count = int(history.get("count", 0))
        except (TypeError, ValueError):
            count = 0
    merged["_resume"] = {
        "count": count + 1,
        "resumed_at": _utc_now_iso(),
        "from_step": from_step,
        "config_changes": changes,
    }
    return merged


def _normalize_grouping(label: str, value: Any) -> str:
    """Validate ``group=`` / ``job_type=``: a short single-line label, or "".

    Anything the server would reject outright (a control character, 256+
    bytes) is rejected here instead, where the traceback still points at the
    call site -- but an empty or absent value is simply "no grouping", which
    is what every run written before this existed has.
    """
    if value is None:
        return ""
    if not isinstance(value, str):
        raise TypeError(f"{label} must be a string, got {type(value).__name__}")
    text = value.strip()
    if not text:
        return ""
    if len(text.encode("utf-8")) > _MAX_GROUPING_BYTES:
        raise ValueError(f"{label} must be at most {_MAX_GROUPING_BYTES} bytes")
    if any(ord(ch) < 0x20 or ord(ch) == 0x7F for ch in text):
        raise ValueError(f"{label} must not contain control characters")
    return text


def init(
    project: str,
    name: str | None = None,
    config: Any = None,
    resume: Any = "never",
    group: str | None = None,
    job_type: str | None = None,
    mode: str | None = None,
    **kwargs: Any,
) -> _Run:
    """Start (or continue) a run. Mirrors ``trackio.init`` / ``wandb.init``.

    ``group`` names the sweep this run is part of and ``job_type`` the role it
    plays in it ("train", "eval", ...), exactly as wandb spells them. Runs
    sharing a ``group`` are collapsed into one foldable row in the run table
    and compared axis-by-axis in the parallel-coordinates view; a run without
    one is listed flat, as before. Both are recorded on the run itself, and a
    later batch that does not repeat them leaves them alone.

    ``resume`` decides what happens when the project already has a run called
    ``name``:

    ``"never"`` (default)
        Never write into an existing run. A name that is taken gets a
        ``-1`` / ``-2`` / ... suffix and a warning, so a restarted job logs a
        second curve instead of interleaving itself into the first one -- and
        so nothing this shim does can abort a training script.
    ``"allow"`` (also ``resume=True``)
        Continue the existing run if there is one, otherwise start it.
    ``"must"``
        Continue the existing run, and raise ``RuntimeError`` if it does not
        exist (or cannot be looked up).

    Continuing a run means: steps carry on from the server's ``last_step``
    rather than restarting at 0 (online only; see ``mode`` below), the run's
    status goes back to ``running`` on
    the first flush, and ``config`` is merged with the previous attempt's
    (new values win; the differences are recorded under ``_resume``).

    ``config`` is a mapping, an ``argparse.Namespace`` or a dataclass
    instance. Values JSON cannot carry are converted when sent (a ``Path`` to
    its string, an ``Enum`` to its value, a numpy scalar to a number, NaN to
    ``"nan"``, ...; anything unrecognised to ``str(value)``, with one warning
    naming the keys), so one odd value never costs the run its whole config.

    ``mode`` is ``"online"`` (the default) or ``"offline"``; it defaults to
    ``THINKINGFACE_MODE``. An offline run makes no network request at all: it
    is written to a directory under ``THINKINGFACE_OFFLINE_DIR`` (default
    ``./thinkingface-offline``) that ``tf experiments sync`` uploads later,
    and the repository is ``THINKINGFACE_REPO`` or, when unset, resolved at
    sync time. ``resume`` is recorded and applied by the sync -- but step
    numbering is not continued: offline there is no server to ask for the
    run's ``last_step``, so auto-numbered steps start at 0 and the sync does
    not shift them. Pass explicit ``step=`` to ``log()`` if an offline run
    continues an existing one (a warning says so once per run otherwise).

    Extra keyword arguments are accepted and ignored -- so a call site written
    against trackio/wandb (e.g. passing ``tags=``) keeps working -- but each
    one is reported with a warning: silently dropping ``tags=`` means a script
    ported from wandb loses its tags with nothing at all to show for it.
    """
    global _current_run

    if kwargs:
        warnings.warn(
            "thinkingface.trackio: init() ignored unsupported argument(s): "
            f"{', '.join(sorted(kwargs))}. They are accepted for wandb/trackio "
            "compatibility but have no effect here."
        )

    resume_mode = _normalize_resume(resume)
    run_mode = _normalize_mode(mode)
    offline = run_mode == "offline"
    group_name = _normalize_grouping("group", group)
    job_type_name = _normalize_grouping("job_type", job_type)
    config_dict = _sanitize.to_config_dict(config)
    endpoint = os.environ.get("THINKINGFACE_ENDPOINT", _DEFAULT_ENDPOINT).rstrip("/")
    token = os.environ.get("THINKINGFACE_TOKEN")
    repo: str | None = os.environ.get("THINKINGFACE_REPO") or None
    repo_resolved = True
    if offline:
        # No /me lookup: the syncer resolves "{user}/trackio-metrics" itself.
        pass
    elif repo is None:
        repo = _resolve_default_repo(endpoint, token)
        if repo is None:
            repo, repo_resolved = _FALLBACK_REPO, False
    run_name = name or f"run-{int(time.time())}"

    if _current_run is not None and not _current_run._finished:
        warnings.warn(
            "thinkingface.trackio: init() called while a previous run "
            f"({_current_run.name!r}) is still active; finishing it first."
        )
        _current_run.finish()

    merged_config = config_dict
    if not _env_meta.is_disabled():
        try:
            meta = _env_meta.collect()
        except Exception:  # metadata collection must never break init()
            meta = {}
        if meta:
            merged_config["_meta"] = meta

    # An auto-generated name cannot collide, so the default path stays exactly
    # as offline-friendly as it was: no request, no warning when the server is
    # unreachable. The offline mode never looks: `tf experiments sync`
    # applies the same resume rules when it uploads the run.
    existing: dict[str, Any] | None = None
    taken: set[str] = set()
    lookup_error: Exception | None = None
    if offline:
        pass
    elif resume_mode != "never" or name is not None:
        try:
            existing, taken = _fetch_run(endpoint, token, repo, project, run_name)
        except Exception as exc:
            lookup_error = exc

    if offline:
        pass
    elif resume_mode == "must":
        if lookup_error is not None:
            raise RuntimeError(
                f'thinkingface.trackio: resume="must" but run {run_name!r} in project '
                f"{project!r} could not be looked up ({lookup_error!r})."
            )
        if existing is None:
            raise RuntimeError(
                f'thinkingface.trackio: resume="must" but run {run_name!r} does not '
                f"exist in project {project!r}."
            )
    elif resume_mode == "never" and existing is not None:
        run_name = _unique_run_name(run_name, taken)
        warnings.warn(
            f"thinkingface.trackio: run {name!r} already exists in project {project!r} "
            f'and resume="never"; logging to {run_name!r} instead. Pass '
            'resume="allow" to continue the existing run.'
        )
        existing = None
    elif resume_mode != "never" and lookup_error is not None:
        # "allow" degrades to "start fresh under this name", which is what the
        # ingest API does anyway: it appends to whatever run the name resolves
        # to. Only the step continuation is lost, hence the warning.
        warnings.warn(
            f"thinkingface.trackio: could not check whether run {run_name!r} already "
            f"exists ({lookup_error!r}); logging from step 0."
        )

    start_step = 0
    if existing is not None:
        try:
            start_step = int(existing.get("last_step") or 0) + 1
        except (TypeError, ValueError):
            start_step = 0
        merged_config = _merge_resumed_config(existing.get("config"), merged_config, start_step)

    _current_run = _Run(
        endpoint,
        token,
        repo,
        project,
        run_name,
        merged_config,
        start_step=start_step,
        resumed=existing is not None,
        group=group_name,
        job_type=job_type_name,
        mode=run_mode,
        resume_mode=resume_mode,
        repo_resolved=repo_resolved,
    )
    return _current_run


def log(metrics: dict[str, Any], step: int | None = None) -> None:
    """Log a dict of metrics for the current run.

    Buffered in-process and flushed every 5 seconds or every 100 points,
    whichever comes first.

    A ``trackio.Image`` or ``trackio.Table`` value is not a metric: it is
    written to a file and committed as the artifact
    ``media/{key}/step_{step:08d}.png`` / ``tables/{key}/step_{step:08d}.parquet``,
    and the point itself carries no value for that key.
    """
    if _current_run is None:
        warnings.warn("thinkingface.trackio: log() called before init(); ignoring.")
        return
    _current_run.log(metrics, step=step)


def log_artifact(path: Any, name: str | None = None) -> None:
    """Attach a file or directory to the current run.

    Mirrors ``trackio.log_artifact`` / ``wandb.log_artifact``'s file-path
    form. The file is committed to the run's experiment *dataset* repository
    under ``{project}/artifacts/{run}/{name}`` (``name`` defaults to the
    file's own basename, and a directory keeps its internal layout under it),
    so it is git-versioned, shows up in ``git clone``, and is readable
    straight out of the bucket at its content-addressed key like everything
    else in the repository. Files large enough for the repository's
    ``.gitattributes`` go over LFS automatically.

    Nothing is uploaded here: staged artifacts are committed together in the
    background every ``THINKINGFACE_ARTIFACT_INTERVAL`` seconds (60 by
    default) while anything is pending, by ``save()``, and finally by
    ``finish()`` -- so a run that saves twenty plots a minute makes one
    commit a minute rather than twenty. A bad path or a name that cannot be
    used (``..``, or the reserved ``metrics.parquet``) is a warning, never an
    exception. In offline mode the file is copied into the run directory
    right away, and committed when the directory is synced.
    """
    if _current_run is None:
        warnings.warn("thinkingface.trackio: log_artifact() called before init(); ignoring.")
        return
    _current_run.log_artifact(path, name=name)


def save() -> None:
    """Commit the current run's pending artifacts and media now.

    Without it they go out on the next background interval
    (``THINKINGFACE_ARTIFACT_INTERVAL``) or at ``finish()``. Never raises; a
    failed commit is retried later.
    """
    if _current_run is None:
        warnings.warn("thinkingface.trackio: save() called before init(); ignoring.")
        return
    _current_run.save()


def log_model(repo_id: str, revision: str | None = None) -> None:
    """Record that this run produced the model at ``repo_id``.

    ``repo_id`` is a model repository as ``"namespace/name"``. With no
    ``revision`` the current HEAD of its default branch is resolved, which is
    what a training job wants right after pushing: "the model as it is now".

    The link is stored as a run annotation, not as a config value and not in
    the repository card, so re-indexing the project's parquet leaves it in
    place and no README has to be edited by hand. It is sent with the rest of
    the run's bookkeeping when ``finish()`` runs, and shows up on both ends:
    the run page links to the model, and the model's lineage view links back
    to the run. A model that does not exist (a typo, or a push that never
    happened) is still recorded and shown with a warning rather than dropped.
    """
    if _current_run is None:
        warnings.warn("thinkingface.trackio: log_model() called before init(); ignoring.")
        return
    _current_run.log_model(repo_id, revision=revision)


def finish(status: str = "finished") -> None:
    """Flush any buffered points and mark the current run as finished.

    Also commits everything ``log_artifact`` staged and records what
    ``log_model`` declared.
    """
    if _current_run is None:
        return
    _current_run.finish(status=status)


@atexit.register
def _flush_on_exit() -> None:
    if _current_run is not None and not _current_run._finished:
        _current_run.finish()
