"""Tests for thinkingface._system_metrics and its wiring into thinkingface.trackio.

The primary contract under test (see todo/exp-system-metrics.md) is that
collection is entirely best-effort: on a machine with no GPU and no
``psutil`` installed -- the state of the test environment itself, verified
below -- collect() must return {} rather than raising, and every key it
does produce must live under the "system/" namespace. `THINKINGFACE_SYSTEM_METRICS=off`
must disable collection outright, and the background sampling wired into
`thinkingface.trackio._Run` must piggyback on the existing flush-timer
thread (rather than start a second one) and stop reliably when the run
finishes.
"""

from __future__ import annotations

import subprocess
import sys
import threading
import time

import pytest

import thinkingface.trackio as trackio_module
from thinkingface import _system_metrics


class TestIsDisabled:
    def test_unset_is_enabled(self, monkeypatch):
        monkeypatch.delenv("THINKINGFACE_SYSTEM_METRICS", raising=False)
        assert _system_metrics.is_disabled() is False

    def test_off_disables(self, monkeypatch):
        monkeypatch.setenv("THINKINGFACE_SYSTEM_METRICS", "off")
        assert _system_metrics.is_disabled() is True

    def test_off_is_case_insensitive_and_trims_whitespace(self, monkeypatch):
        monkeypatch.setenv("THINKINGFACE_SYSTEM_METRICS", " OFF ")
        assert _system_metrics.is_disabled() is True

    def test_other_values_are_enabled(self, monkeypatch):
        monkeypatch.setenv("THINKINGFACE_SYSTEM_METRICS", "on")
        assert _system_metrics.is_disabled() is False


class TestCollectGpuNoHardware:
    """The test environment itself has neither torch nor nvidia-smi (see
    module docstring), so these exercise the real "no GPU" fallback path
    with no mocking at all."""

    def test_torch_not_installed(self):
        assert "torch" not in sys.modules or not hasattr(sys.modules.get("torch"), "cuda")

    def test_nvidia_smi_not_on_path(self):
        import shutil

        assert shutil.which("nvidia-smi") is None

    def test_collect_gpu_returns_empty_without_raising(self):
        assert _system_metrics._collect_gpu() == {}

    def test_collect_never_raises_and_is_empty(self):
        assert _system_metrics.collect() == {}


class TestCollectGpuTorchFailureModes:
    def test_torch_import_error_falls_back_to_nvidia_smi_path(self, monkeypatch):
        # No real torch in this environment, so _collect_gpu_torch() already
        # raises ModuleNotFoundError naturally; assert the outer function
        # swallows it and still attempts (and gracefully fails) nvidia-smi.
        def _raise(*args, **kwargs):
            raise FileNotFoundError("nvidia-smi not found")

        monkeypatch.setattr(_system_metrics.subprocess, "run", _raise)
        assert _system_metrics._collect_gpu() == {}

    def test_torch_present_but_cuda_unavailable_falls_back_to_nvidia_smi(self, monkeypatch):
        fake_torch = _make_fake_torch(cuda_available=False)
        sys.modules["torch"] = fake_torch
        try:
            monkeypatch.setattr(
                _system_metrics,
                "_collect_gpu_nvidia_smi",
                lambda: {"system/gpu.0.util": 1.0},
            )
            assert _system_metrics._collect_gpu() == {"system/gpu.0.util": 1.0}
        finally:
            del sys.modules["torch"]

    def test_torch_raising_mid_collection_is_swallowed(self, monkeypatch):
        class _ExplodingTorch:
            class cuda:  # noqa: N801 - mimics torch.cuda's module-like surface
                @staticmethod
                def is_available():
                    raise RuntimeError("boom")

        sys.modules["torch"] = _ExplodingTorch
        try:
            monkeypatch.setattr(_system_metrics, "_collect_gpu_nvidia_smi", lambda: {})
            assert _system_metrics._collect_gpu() == {}
        finally:
            del sys.modules["torch"]


def _make_fake_torch(cuda_available: bool, device_count: int = 0):
    class _Cuda:
        @staticmethod
        def is_available():
            return cuda_available

        @staticmethod
        def device_count():
            return device_count

    fake = type(sys)("torch")
    fake.cuda = _Cuda
    return fake


class TestCollectGpuTorchSuccess:
    def test_torch_reports_util_mem_and_temp(self, monkeypatch):
        class _Cuda:
            @staticmethod
            def is_available():
                return True

            @staticmethod
            def device_count():
                return 1

            @staticmethod
            def utilization(_i):
                return 42

            @staticmethod
            def mem_get_info(_i):
                total = 8 * 1024 * 1024 * 1024
                free = 6 * 1024 * 1024 * 1024
                return free, total

            @staticmethod
            def temperature(_i):
                return 55

        fake_torch = type(sys)("torch")
        fake_torch.cuda = _Cuda
        sys.modules["torch"] = fake_torch
        try:
            metrics = _system_metrics._collect_gpu_torch()
        finally:
            del sys.modules["torch"]

        assert metrics["system/gpu.0.util"] == 42.0
        assert metrics["system/gpu.0.temp_c"] == 55.0
        assert metrics["system/gpu.0.mem_used_mb"] == pytest.approx(2048.0)
        assert metrics["system/gpu.0.mem_total_mb"] == pytest.approx(8192.0)
        assert metrics["system/gpu.0.mem_percent"] == pytest.approx(25.0)

    def test_one_field_failing_does_not_drop_the_others(self, monkeypatch):
        class _Cuda:
            @staticmethod
            def is_available():
                return True

            @staticmethod
            def device_count():
                return 1

            @staticmethod
            def utilization(_i):
                raise RuntimeError("older torch build has no utilization()")

            @staticmethod
            def mem_get_info(_i):
                total = 100 * 1024 * 1024
                return 0, total  # 0 bytes free -> 100 MB used

            @staticmethod
            def temperature(_i):
                raise RuntimeError("older torch build has no temperature()")

        fake_torch = type(sys)("torch")
        fake_torch.cuda = _Cuda
        sys.modules["torch"] = fake_torch
        try:
            metrics = _system_metrics._collect_gpu_torch()
        finally:
            del sys.modules["torch"]

        assert "system/gpu.0.util" not in metrics
        assert "system/gpu.0.temp_c" not in metrics
        assert metrics["system/gpu.0.mem_used_mb"] == pytest.approx(100.0)


class TestCollectGpuNvidiaSmi:
    def test_parses_csv_output_into_namespaced_keys(self, monkeypatch):
        def _fake_run(cmd, **kwargs):
            assert cmd[0] == "nvidia-smi"
            return subprocess.CompletedProcess(
                cmd,
                returncode=0,
                stdout="42, 2048, 8192, 55\n17, 1024, 8192, 50\n",
                stderr="",
            )

        monkeypatch.setattr(_system_metrics.subprocess, "run", _fake_run)
        metrics = _system_metrics._collect_gpu_nvidia_smi()

        assert metrics["system/gpu.0.util"] == 42.0
        assert metrics["system/gpu.0.mem_used_mb"] == 2048.0
        assert metrics["system/gpu.0.mem_total_mb"] == 8192.0
        assert metrics["system/gpu.0.temp_c"] == 55.0
        assert metrics["system/gpu.0.mem_percent"] == pytest.approx(25.0)
        assert metrics["system/gpu.1.util"] == 17.0

    def test_nonzero_return_code_yields_empty(self, monkeypatch):
        def _fake_run(cmd, **kwargs):
            return subprocess.CompletedProcess(cmd, returncode=1, stdout="", stderr="no devices")

        monkeypatch.setattr(_system_metrics.subprocess, "run", _fake_run)
        assert _system_metrics._collect_gpu_nvidia_smi() == {}

    def test_missing_binary_is_swallowed_by_collect_gpu(self, monkeypatch):
        def _raise(*args, **kwargs):
            raise FileNotFoundError("no such file: nvidia-smi")

        monkeypatch.setattr(_system_metrics.subprocess, "run", _raise)
        assert _system_metrics._collect_gpu() == {}

    def test_timeout_is_swallowed_by_collect_gpu(self, monkeypatch):
        def _raise(*args, **kwargs):
            raise subprocess.TimeoutExpired(cmd="nvidia-smi", timeout=3)

        monkeypatch.setattr(_system_metrics.subprocess, "run", _raise)
        assert _system_metrics._collect_gpu() == {}

    def test_malformed_rows_are_skipped_not_fatal(self, monkeypatch):
        def _fake_run(cmd, **kwargs):
            return subprocess.CompletedProcess(
                cmd, returncode=0, stdout="not, enough, columns\n", stderr=""
            )

        monkeypatch.setattr(_system_metrics.subprocess, "run", _fake_run)
        assert _system_metrics._collect_gpu_nvidia_smi() == {}


class TestCollectProcess:
    def test_no_psutil_returns_empty(self):
        # psutil is not installed in this test environment (see module
        # docstring), so this exercises the real ImportError path.
        assert _system_metrics._collect_process() == {}

    def test_psutil_available_yields_namespaced_float_keys(self, monkeypatch):
        fake_psutil = type(sys)("psutil")

        def _cpu_percent(interval=None):
            assert interval is None  # must never block the flush thread
            return 12.5

        class _Process:
            @staticmethod
            def memory_info():
                return type("MemInfo", (), {"rss": 256 * 1024 * 1024})()

        fake_psutil.cpu_percent = _cpu_percent
        fake_psutil.Process = _Process
        sys.modules["psutil"] = fake_psutil
        try:
            metrics = _system_metrics._collect_process()
        finally:
            del sys.modules["psutil"]

        assert metrics["system/cpu.percent"] == 12.5
        assert metrics["system/mem.rss_mb"] == pytest.approx(256.0)

    def test_psutil_failure_is_swallowed(self, monkeypatch):
        fake_psutil = type(sys)("psutil")

        def _raise(*args, **kwargs):
            raise RuntimeError("boom")

        fake_psutil.cpu_percent = _raise
        fake_psutil.Process = _raise
        sys.modules["psutil"] = fake_psutil
        try:
            assert _system_metrics._collect_process() == {}
        finally:
            del sys.modules["psutil"]


class TestKeyShape:
    def test_every_produced_key_is_namespaced(self, monkeypatch):
        monkeypatch.setattr(
            _system_metrics,
            "_collect_gpu",
            lambda: {"system/gpu.0.util": 1.0, "system/gpu.0.mem_percent": 2.0},
        )
        monkeypatch.setattr(
            _system_metrics,
            "_collect_process",
            lambda: {"system/cpu.percent": 3.0, "system/mem.rss_mb": 4.0},
        )
        metrics = _system_metrics.collect()
        assert metrics
        assert all(key.startswith(_system_metrics.SYSTEM_METRIC_PREFIX) for key in metrics)

    def test_collect_survives_a_misbehaving_collector(self, monkeypatch):
        def _raise():
            raise RuntimeError("boom")

        monkeypatch.setattr(_system_metrics, "_collect_gpu", _raise)
        monkeypatch.setattr(
            _system_metrics, "_collect_process", lambda: {"system/cpu.percent": 1.0}
        )
        assert _system_metrics.collect() == {"system/cpu.percent": 1.0}


class TestRunIntegration:
    """Exercises the wiring in thinkingface.trackio._Run: throttled
    piggybacking on the flush timer, no interference with the run's own
    step counter, the off switch, and that the background thread stops
    reliably on finish()."""

    def _make_run(self, monkeypatch, *, flush_interval: float = 60.0) -> trackio_module._Run:
        # A long flush interval keeps the real timer from firing mid-test;
        # every test below drives collection manually.
        monkeypatch.setattr(trackio_module, "_FLUSH_INTERVAL_SECONDS", flush_interval)
        return trackio_module._Run(
            endpoint="http://localhost:8080",
            token=None,
            repo="alice/exp",
            project="proj",
            name="run-1",
            config={},
        )

    def test_system_metrics_enabled_by_default(self, monkeypatch):
        monkeypatch.delenv("THINKINGFACE_SYSTEM_METRICS", raising=False)
        run = self._make_run(monkeypatch)
        try:
            assert run._system_metrics_enabled is True
        finally:
            run._timer.cancel()

    def test_off_env_var_disables_collection(self, monkeypatch):
        monkeypatch.setenv("THINKINGFACE_SYSTEM_METRICS", "off")
        run = self._make_run(monkeypatch)
        try:
            assert run._system_metrics_enabled is False
            run._maybe_collect_system_metrics()
            assert run._buffer == []
        finally:
            run._timer.cancel()

    def test_collected_metrics_are_buffered_without_advancing_step(self, monkeypatch):
        run = self._make_run(monkeypatch)
        try:
            run.step = 7
            monkeypatch.setattr(_system_metrics, "collect", lambda: {"system/cpu.percent": 33.0})
            run._maybe_collect_system_metrics()

            assert len(run._buffer) == 1
            point = run._buffer[0]
            assert point["metrics"] == {"system/cpu.percent": 33.0}
            assert point["step"] == 7
            # Unlike log(), sampling must not consume a step number.
            assert run.step == 7
        finally:
            run._timer.cancel()

    def test_empty_collection_result_is_not_buffered(self, monkeypatch):
        run = self._make_run(monkeypatch)
        try:
            monkeypatch.setattr(_system_metrics, "collect", lambda: {})
            run._maybe_collect_system_metrics()
            assert run._buffer == []
        finally:
            run._timer.cancel()

    def test_a_raising_collector_never_propagates(self, monkeypatch):
        run = self._make_run(monkeypatch)
        try:

            def _raise():
                raise RuntimeError("boom")

            monkeypatch.setattr(_system_metrics, "collect", _raise)
            run._maybe_collect_system_metrics()  # must not raise
            assert run._buffer == []
        finally:
            run._timer.cancel()

    def test_throttled_to_roughly_once_per_default_interval(self, monkeypatch):
        run = self._make_run(monkeypatch)
        try:
            calls = []
            monkeypatch.setattr(
                _system_metrics, "collect", lambda: calls.append(1) or {"system/cpu.percent": 1.0}
            )

            fake_now = [0.0]
            monkeypatch.setattr(time, "monotonic", lambda: fake_now[0])

            run._maybe_collect_system_metrics()
            assert len(calls) == 1  # first tick always samples

            fake_now[0] += 1.0  # well under DEFAULT_INTERVAL_SECONDS (10s)
            run._maybe_collect_system_metrics()
            assert len(calls) == 1  # throttled, no second sample yet

            fake_now[0] += _system_metrics.DEFAULT_INTERVAL_SECONDS
            run._maybe_collect_system_metrics()
            assert len(calls) == 2  # interval elapsed, samples again
        finally:
            run._timer.cancel()

    def test_flush_timer_thread_stops_on_finish(self, monkeypatch):
        run = self._make_run(monkeypatch)
        assert isinstance(run._timer, threading.Timer)
        assert run._timer.is_alive()

        run.finish()

        run._timer.join(timeout=2.0)
        assert not run._timer.is_alive()
        assert run._finished is True

    def test_no_collection_after_finish(self, monkeypatch):
        run = self._make_run(monkeypatch)
        run.finish()

        calls = []
        monkeypatch.setattr(
            _system_metrics, "collect", lambda: calls.append(1) or {"system/cpu.percent": 1.0}
        )
        run._maybe_collect_system_metrics()
        assert calls == []
