"""Best-effort collection of GPU / CPU / memory telemetry for ``thinkingface.trackio``.

This answers a different question than ``thinkingface._env_meta``: not "what
produced this run" but "what was the machine doing while it ran" -- GPU
utilization, GPU memory, GPU temperature, host CPU load, and this process's
resident memory. Without it, a run whose loss plateaus for GPU-bound reasons
(a saturated GPU, memory pressure short of OOM) is indistinguishable from one
that plateaus for algorithmic reasons (see ``todo/exp-system-metrics.md``).

Every collector here is best-effort in exactly the same sense as
``_env_meta``: a missing ``torch``, a missing ``nvidia-smi`` binary, a
missing (optional) ``psutil``, a machine with no GPU, or any other
environmental quirk simply drops that one key rather than raising.
:func:`collect` itself never raises -- ``thinkingface.trackio``'s background
sampling must never be the thing that breaks a training run.

Keys are namespaced under :data:`SYSTEM_METRIC_PREFIX` (``"system/"``), e.g.
``system/gpu.0.util``, ``system/cpu.percent``, ``system/mem.rss_mb``, so a
chart layer can group them separately from the metrics a run actually logs.

Set ``THINKINGFACE_SYSTEM_METRICS=off`` to disable collection entirely (see
:func:`is_disabled`).
"""

from __future__ import annotations

import subprocess
from os import environ

#: Default interval, in seconds, between background samples. The trackio
#: shim piggybacks this onto its existing flush timer (see
#: ``thinkingface.trackio._Run._maybe_collect_system_metrics``) rather than
#: running a second thread on its own schedule.
DEFAULT_INTERVAL_SECONDS = 10.0

#: Every key this module produces starts with this prefix, so the backend /
#: frontend can split "system telemetry" out from the metrics a run logs
#: itself (see frontend/components/experiments/metrics-charts.tsx).
SYSTEM_METRIC_PREFIX = "system/"

_SUBPROCESS_TIMEOUT_SECONDS = 3.0

__all__ = [
    "DEFAULT_INTERVAL_SECONDS",
    "SYSTEM_METRIC_PREFIX",
    "is_disabled",
    "collect",
]


def is_disabled() -> bool:
    """Whether ``THINKINGFACE_SYSTEM_METRICS=off`` opts out of collection."""
    return environ.get("THINKINGFACE_SYSTEM_METRICS", "").strip().lower() == "off"


def _collect_gpu_torch() -> dict[str, float]:
    """GPU util/memory/temperature via torch's CUDA bindings.

    Each field is collected independently: older torch builds lack
    ``torch.cuda.utilization`` / ``torch.cuda.temperature`` (both shell out
    to ``nvidia-smi`` internally), so a missing attribute there should not
    also drop the memory figures, which come from ``torch.cuda.mem_get_info``
    directly.
    """
    import torch  # type: ignore[import-not-found]

    if not torch.cuda.is_available():
        return {}

    metrics: dict[str, float] = {}
    for i in range(torch.cuda.device_count()):
        prefix = f"{SYSTEM_METRIC_PREFIX}gpu.{i}."

        try:
            metrics[f"{prefix}util"] = float(torch.cuda.utilization(i))
        except Exception:
            pass

        try:
            free_bytes, total_bytes = torch.cuda.mem_get_info(i)
            used_bytes = total_bytes - free_bytes
            metrics[f"{prefix}mem_used_mb"] = used_bytes / (1024 * 1024)
            metrics[f"{prefix}mem_total_mb"] = total_bytes / (1024 * 1024)
            if total_bytes:
                metrics[f"{prefix}mem_percent"] = 100.0 * used_bytes / total_bytes
        except Exception:
            pass

        try:
            metrics[f"{prefix}temp_c"] = float(torch.cuda.temperature(i))
        except Exception:
            pass

    return metrics


def _collect_gpu_nvidia_smi() -> dict[str, float]:
    """GPU util/memory/temperature by shelling out to ``nvidia-smi`` directly.

    Used when torch is not installed, or is installed without CUDA support
    (in which case it cannot see the GPU at all, so there is nothing for the
    torch path above to report).
    """
    result = subprocess.run(
        [
            "nvidia-smi",
            "--query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu",
            "--format=csv,noheader,nounits",
        ],
        capture_output=True,
        text=True,
        timeout=_SUBPROCESS_TIMEOUT_SECONDS,
    )
    if result.returncode != 0:
        return {}

    metrics: dict[str, float] = {}
    for i, line in enumerate(result.stdout.splitlines()):
        if not line.strip():
            continue
        parts = [p.strip() for p in line.split(",")]
        if len(parts) != 4:
            continue
        util, mem_used_mb, mem_total_mb, temp_c = parts
        prefix = f"{SYSTEM_METRIC_PREFIX}gpu.{i}."

        try:
            metrics[f"{prefix}util"] = float(util)
        except ValueError:
            pass

        used_mb = total_mb = None
        try:
            used_mb = float(mem_used_mb)
            metrics[f"{prefix}mem_used_mb"] = used_mb
        except ValueError:
            pass
        try:
            total_mb = float(mem_total_mb)
            metrics[f"{prefix}mem_total_mb"] = total_mb
        except ValueError:
            pass
        if used_mb is not None and total_mb:
            metrics[f"{prefix}mem_percent"] = 100.0 * used_mb / total_mb

        try:
            metrics[f"{prefix}temp_c"] = float(temp_c)
        except ValueError:
            pass

    return metrics


def _collect_gpu() -> dict[str, float]:
    """GPU metrics via torch, falling back to ``nvidia-smi``.

    Returns ``{}`` silently -- never raises -- when neither is available,
    which is the common case on a CPU-only machine.
    """
    try:
        metrics = _collect_gpu_torch()
        if metrics:
            return metrics
    except Exception:
        pass

    try:
        return _collect_gpu_nvidia_smi()
    except Exception:
        return {}


def _collect_process() -> dict[str, float]:
    """Host CPU load and this process's resident memory, via ``psutil``.

    ``psutil`` is an optional dependency (not in ``dependencies`` in
    pyproject.toml); when it is not installed this returns ``{}`` and the
    corresponding keys are simply absent, per the "on a machine with no
    GPU/CPU support the key is just missing" contract in
    todo/exp-system-metrics.md.
    """
    try:
        import psutil  # type: ignore[import-not-found]
    except Exception:
        return {}

    metrics: dict[str, float] = {}

    try:
        # interval=None: an instantaneous, non-blocking reading relative to
        # the last call (or 0.0 on the very first call). A blocking interval
        # here would stall the flush-timer thread that drives this.
        metrics[f"{SYSTEM_METRIC_PREFIX}cpu.percent"] = float(psutil.cpu_percent(interval=None))
    except Exception:
        pass

    try:
        rss_bytes = psutil.Process().memory_info().rss
        metrics[f"{SYSTEM_METRIC_PREFIX}mem.rss_mb"] = rss_bytes / (1024 * 1024)
    except Exception:
        pass

    return metrics


def collect() -> dict[str, float]:
    """Collect everything this module knows how to, best-effort.

    Returns a flat dict of ``system/``-prefixed metric names to float
    values, meant to be passed straight to a ``trackio.log()``-shaped call.
    Each collector is independently wrapped, so one failing (or finding
    nothing -- no GPU, no ``psutil``) never drops the others, and this
    function itself never raises.
    """
    metrics: dict[str, float] = {}

    try:
        metrics.update(_collect_gpu())
    except Exception:
        pass

    try:
        metrics.update(_collect_process())
    except Exception:
        pass

    return metrics
