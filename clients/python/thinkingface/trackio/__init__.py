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

``log_artifact(path, name=None)`` attaches a file (or a directory) to the
run. It goes into the same dataset repository as the metrics, under
``{project}/artifacts/{run}/{name}``, through the ordinary
preupload/commit endpoints -- so an artifact is git-versioned content,
reachable by ``git clone`` and, at its content-addressed bucket key, by
``gcloud storage cp`` (see the repository's ``GET .../gcs/{rev}`` API), with
large files routed to LFS by ``.gitattributes``. Everything a run logs is
committed once, at ``finish()``.

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
the server's ``last_step``, the status goes back to ``running``, and the
configs are merged; see ``init`` for the full contract.

A network failure never raises into the caller: points are logged as a
warning and kept for the next flush attempt, so a flaky connection or a
temporarily unreachable server does not abort a training run. The one
exception is ``resume="must"``, which cannot be honoured without reaching
the server and so raises rather than silently starting from zero.

.. _trackio: https://github.com/gradio-app/trackio
"""

from __future__ import annotations

import atexit
import os
import threading
import time
import warnings
from datetime import datetime, timezone
from typing import Any
from urllib.parse import quote

import requests

from thinkingface import _env_meta, _system_metrics
from thinkingface.trackio import _artifacts

_DEFAULT_ENDPOINT = "http://localhost:8080"
_FLUSH_INTERVAL_SECONDS = 5.0
_FLUSH_MAX_POINTS = 100
_REQUEST_TIMEOUT_SECONDS = 10.0
# Ceiling on the retry buffer. A training run that logs for hours against an
# unreachable server would otherwise keep every point in memory forever; past
# this many points the oldest are dropped (with a warning) so logging can never
# be the thing that OOMs the run.
_BUFFER_MAX_POINTS = 10_000
# Ceiling on `group=` / `job_type=`, matching the server's own limit on the
# free-text ingest names (maxIngestNameBytes in internal/api/experiments.go).
_MAX_GROUPING_BYTES = 256

__all__ = ["init", "log", "log_artifact", "log_model", "finish"]


def _utc_now_iso() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


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


class _Run:
    """A single active run: buffers points and flushes them periodically."""

    def __init__(
        self,
        endpoint: str,
        token: str | None,
        repo: str,
        project: str,
        name: str,
        config: dict[str, Any] | None,
        start_step: int = 0,
        resumed: bool = False,
        group: str = "",
        job_type: str = "",
    ) -> None:
        self.endpoint = endpoint.rstrip("/")
        self.token = token
        self.repo = repo
        self.project = project
        self.name = name
        self.config = config or {}
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
        self._finished = False
        self._config_sent = False
        self._timer: threading.Timer | None = None

        # Artifacts and produced models are gathered during the run and sent
        # once, from finish(): every artifact is a git commit, and the model
        # list is a wholesale replace, so batching keeps a run that logs
        # twenty files to one commit and one PATCH.
        self._artifacts: list[tuple[Any, str]] = []
        self._models: list[dict[str, str]] = []
        self._models_dirty = False

        # System metrics (GPU/CPU/memory) piggyback on the flush timer
        # below rather than running a second background thread; see
        # _maybe_collect_system_metrics().
        self._system_metrics_enabled = not _system_metrics.is_disabled()
        self._last_system_metrics_at: float | None = None

        self.namespace, self.repo_name = _split_repo(repo)
        _check_path_segment("project", project)

        self._schedule_flush()

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
        self._maybe_collect_system_metrics()
        self.flush()
        if not self._finished:
            self._schedule_flush()

    # -- background system-metrics sampling --------------------------------

    def _maybe_collect_system_metrics(self) -> None:
        """Sample GPU/CPU/memory telemetry, throttled to roughly once per
        ``_system_metrics.DEFAULT_INTERVAL_SECONDS``.

        Called from the flush-timer thread on every tick (every
        ``_FLUSH_INTERVAL_SECONDS``, 5s by default) rather than from a
        second timer of its own, so the 10s system-metrics cadence is
        approximated by skipping every other tick.
        """
        if not self._system_metrics_enabled or self._finished:
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
        with self._lock:
            if self._finished:
                return
            self._buffer.append(
                {
                    "step": self.step,
                    "timestamp": _utc_now_iso(),
                    "metrics": metrics,
                }
            )

    # -- public API ---------------------------------------------------------

    def log(self, metrics: dict[str, Any], step: int | None = None) -> None:
        if self._finished:
            warnings.warn("thinkingface.trackio: log() called after finish(); ignoring.")
            return
        with self._lock:
            if step is None:
                step = self.step
            self.step = max(self.step, step) + 1
            self._buffer.append(
                {
                    "step": step,
                    "timestamp": _utc_now_iso(),
                    "metrics": dict(metrics),
                }
            )
            should_flush = len(self._buffer) >= _FLUSH_MAX_POINTS
        if should_flush:
            self.flush()

    def flush(self) -> None:
        with self._lock:
            if not self._buffer:
                return
            points, self._buffer = self._buffer, []
            config = None if self._config_sent else self.config

        payload: dict[str, Any] = {
            "run": self.name,
            "status": "running",
            "points": points,
        }
        if config is not None:
            payload["config"] = config
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
        except Exception as exc:  # network failures must never raise
            warnings.warn(
                f"thinkingface.trackio: failed to send {len(points)} point(s) "
                f"for run {self.name!r} ({exc!r}); will retry on next flush."
            )
            self._requeue(points)
            return

        if resp.ok:
            with self._lock:
                self._config_sent = True
            return

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
            self._requeue(points)
        else:
            warnings.warn(
                f"thinkingface.trackio: dropping {len(points)} point(s) for run "
                f"{self.name!r}: server returned {resp.status_code} ({detail!r}). "
                "Check THINKINGFACE_TOKEN / THINKINGFACE_REPO."
            )

    def _requeue(self, points: list[dict[str, Any]]) -> None:
        """Put unsent points back at the front, capped at _BUFFER_MAX_POINTS."""
        with self._lock:
            self._buffer = points + self._buffer
            overflow = len(self._buffer) - _BUFFER_MAX_POINTS
            if overflow > 0:
                # Drop the oldest: the recent tail of a training curve is the
                # part still worth delivering.
                del self._buffer[:overflow]
        if overflow > 0:
            warnings.warn(
                f"thinkingface.trackio: retry buffer full, dropped {overflow} "
                f"oldest point(s) for run {self.name!r}."
            )

    # -- artifacts and produced models -------------------------------------

    def log_artifact(self, path: Any, name: str | None = None) -> None:
        """Stage a file (or a whole directory) for upload at finish()."""
        if self._finished:
            warnings.warn("thinkingface.trackio: log_artifact() called after finish(); ignoring.")
            return
        try:
            staged = _artifacts.stage(path, name)
        except (ValueError, OSError) as exc:
            # A bad name or an unreadable path is the caller's mistake, but
            # this shim never aborts a training script over bookkeeping.
            warnings.warn(f"thinkingface.trackio: log_artifact({path!r}) ignored: {exc}")
            return
        with self._lock:
            self._artifacts.extend(staged)

    def log_model(self, repo_id: str, revision: str | None = None) -> None:
        """Record that this run produced ``repo_id`` at ``revision``."""
        if self._finished:
            warnings.warn("thinkingface.trackio: log_model() called after finish(); ignoring.")
            return
        try:
            namespace, model_name = _split_repo(repo_id)
        except ValueError as exc:
            warnings.warn(f"thinkingface.trackio: log_model({repo_id!r}) ignored: {exc}")
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

    def _upload_artifacts(self) -> None:
        """Commit every staged artifact in one go.

        Uploading goes through ``huggingface_hub``, i.e. through the same
        preupload/commit endpoints any other client uses: large files are
        routed to LFS by the repository's ``.gitattributes``, and the result
        is ordinary git content rather than an opaque blob store.
        """
        with self._lock:
            staged, self._artifacts = self._artifacts, []
        if not staged:
            return
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
            warnings.warn(
                f"thinkingface.trackio: failed to upload {len(staged)} artifact(s) for "
                f"run {self.name!r} ({exc!r}); they were not committed."
            )

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

    def finish(self, status: str = "finished") -> None:
        if self._finished:
            return
        self.flush()
        self._finished = True
        if self._timer is not None:
            self._timer.cancel()
        self._upload_artifacts()
        finish_payload: dict[str, Any] = {"run": self.name, "status": status}
        # Also on finish: a run that logged no points at all is created by
        # this call, so without it such a run would fall out of its sweep.
        if self.group:
            finish_payload["group"] = self.group
        if self.job_type:
            finish_payload["job_type"] = self.job_type
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
                f"thinkingface.trackio: failed to mark run {self.name!r} as "
                f"{status!r} ({exc!r})."
            )
        # After the finish call: that is what guarantees the run row exists,
        # since a run that logged no points at all is created there.
        self._sync_models()


_current_run: _Run | None = None


def _resolve_default_repo(endpoint: str, token: str | None) -> str:
    """Default THINKINGFACE_REPO: "{user}/trackio-metrics"."""
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
        return "unknown/trackio-metrics"


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
            'resume must be one of "allow", "must", "never" (or True/False), ' f"got {resume!r}"
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
        if key not in _RESERVED_CONFIG_KEYS and key in merged and merged[key] != value:
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
    config: dict[str, Any] | None = None,
    resume: Any = "never",
    group: str | None = None,
    job_type: str | None = None,
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
    rather than restarting at 0, the run's status goes back to ``running`` on
    the first flush, and ``config`` is merged with the previous attempt's
    (new values win; the differences are recorded under ``_resume``).

    Extra keyword arguments are accepted and ignored, so call sites written
    against trackio/wandb (e.g. passing ``tags=``) keep working.
    """
    global _current_run

    mode = _normalize_resume(resume)
    group_name = _normalize_grouping("group", group)
    job_type_name = _normalize_grouping("job_type", job_type)
    endpoint = os.environ.get("THINKINGFACE_ENDPOINT", _DEFAULT_ENDPOINT).rstrip("/")
    token = os.environ.get("THINKINGFACE_TOKEN")
    repo = os.environ.get("THINKINGFACE_REPO") or _resolve_default_repo(endpoint, token)
    run_name = name or f"run-{int(time.time())}"

    if _current_run is not None and not _current_run._finished:
        warnings.warn(
            "thinkingface.trackio: init() called while a previous run "
            f"({_current_run.name!r}) is still active; finishing it first."
        )
        _current_run.finish()

    merged_config = dict(config or {})
    if not _env_meta.is_disabled():
        try:
            meta = _env_meta.collect()
        except Exception:  # metadata collection must never break init()
            meta = {}
        if meta:
            merged_config["_meta"] = meta

    # An auto-generated name cannot collide, so the default path stays exactly
    # as offline-friendly as it was: no request, no warning when the server is
    # unreachable.
    existing: dict[str, Any] | None = None
    taken: set[str] = set()
    lookup_error: Exception | None = None
    if mode != "never" or name is not None:
        try:
            existing, taken = _fetch_run(endpoint, token, repo, project, run_name)
        except Exception as exc:
            lookup_error = exc

    if mode == "must":
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
    elif mode == "never" and existing is not None:
        run_name = _unique_run_name(run_name, taken)
        warnings.warn(
            f"thinkingface.trackio: run {name!r} already exists in project {project!r} "
            f'and resume="never"; logging to {run_name!r} instead. Pass '
            'resume="allow" to continue the existing run.'
        )
        existing = None
    elif mode != "never" and lookup_error is not None:
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
    )
    return _current_run


def log(metrics: dict[str, Any], step: int | None = None) -> None:
    """Log a dict of metrics for the current run.

    Buffered in-process and flushed every 5 seconds or every 100 points,
    whichever comes first.
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

    Nothing is uploaded here: every artifact logged by a run is committed
    together when ``finish()`` runs, so a run that saves twenty plots makes
    one commit rather than twenty. A bad path or a name that cannot be used
    (``..``, or the reserved ``metrics.parquet``) is a warning, never an
    exception.
    """
    if _current_run is None:
        warnings.warn("thinkingface.trackio: log_artifact() called before init(); ignoring.")
        return
    _current_run.log_artifact(path, name=name)


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
