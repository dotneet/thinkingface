"""Regression tests: ``_Run.flush()`` must resend ``config`` after it changes.

Before this fix, ``_Run._config_sent`` was set to ``True`` after the first
successful flush and never reset, so any change to ``run.config`` made after
that point -- whether the caller pokes the dict directly, or
``ThinkingFaceLightningLogger.log_hyperparams`` does ``self._run.config.update(
...)`` because ``log_hyperparams`` ran after ``log_metrics`` had already
started the run -- was silently dropped forever: no warning, no error, just a
config that never reaches the server.

``_Run.flush()`` now compares ``self.config`` against a snapshot of whatever
was last sent successfully, and resends only when they differ. This keeps
``run.config`` a plain, directly-mutable ``dict`` (the shape
``ThinkingFaceLightningLogger`` and any external caller already rely on --
see ``thinkingface/trackio/integrations.py``), so no public API changes.

No server is involved: ``requests.get`` / ``requests.post`` are stubbed.
"""

from __future__ import annotations

import sys
import threading
import types
from typing import Any

import pytest

import thinkingface.trackio as trackio


class _FakeResponse:
    def __init__(self, payload: Any = None, status_code: int = 200) -> None:
        self._payload = payload if payload is not None else {}
        self.status_code = status_code
        self.ok = 200 <= status_code < 300
        self.text = ""

    def json(self) -> Any:
        return self._payload

    def raise_for_status(self) -> None:
        if not self.ok:
            raise RuntimeError(f"HTTP {self.status_code}")


@pytest.fixture(autouse=True)
def _isolated_env(monkeypatch):
    monkeypatch.setenv("THINKINGFACE_ENDPOINT", "http://localhost:8080")
    monkeypatch.setenv("THINKINGFACE_REPO", "alice/exp")
    monkeypatch.setenv("THINKINGFACE_META", "off")
    monkeypatch.setenv("THINKINGFACE_SYSTEM_METRICS", "off")
    monkeypatch.delenv("THINKINGFACE_TOKEN", raising=False)
    monkeypatch.setattr(trackio, "_FLUSH_INTERVAL_SECONDS", 3600.0)
    yield
    run = trackio._current_run
    if run is not None:
        if run._timer is not None:
            run._timer.cancel()
        run._finished = True
        trackio._current_run = None


@pytest.fixture
def server(monkeypatch):
    """A stand-in for the experiment API: an empty run table + a request log.

    ``state["fail_next"]`` lets a test make exactly one upcoming POST fail
    with a retryable 500, to exercise "the config isn't lost on a failed
    flush".
    """

    state: dict[str, Any] = {"runs": [], "posts": [], "fail_next": False}

    def fake_get(url, headers=None, timeout=None):
        return _FakeResponse({"runs": state["runs"]})

    def fake_post(url, json=None, headers=None, timeout=None):
        state["posts"].append((url, json))
        if state["fail_next"]:
            state["fail_next"] = False
            return _FakeResponse(status_code=500)
        return _FakeResponse({"ok": True})

    monkeypatch.setattr(trackio.requests, "get", fake_get)
    monkeypatch.setattr(trackio.requests, "post", fake_post)
    return state


def _payloads(state, suffix: str) -> list[dict[str, Any]]:
    return [body for url, body in state["posts"] if url.endswith(suffix)]


class TestConfigResendOnChange:
    def test_config_changed_after_first_flush_is_resent(self, server):
        run = trackio.init("proj", name="r1", config={"lr": 0.1})
        run.log({"loss": 0.5})
        run.flush()
        first, *_ = _payloads(server, "/log")
        assert first["config"] == {"lr": 0.1}

        # Mutated directly on the dict, the same way
        # ThinkingFaceLightningLogger.log_hyperparams does via
        # `self._run.config.update(...)` once a run is already live.
        run.config["lr"] = 0.05
        run.log({"loss": 0.4})
        run.flush()

        batches = _payloads(server, "/log")
        assert len(batches) == 2
        assert batches[1]["config"] == {"lr": 0.05}

    def test_unchanged_config_is_not_resent(self, server):
        run = trackio.init("proj", name="r2", config={"lr": 0.1})
        run.log({"loss": 0.5})
        run.flush()
        run.log({"loss": 0.4})
        run.flush()

        batches = _payloads(server, "/log")
        assert len(batches) == 2
        assert "config" in batches[0]
        assert "config" not in batches[1]

    def test_config_is_not_lost_when_a_flush_fails(self, server):
        run = trackio.init("proj", name="r3", config={"lr": 0.1})
        run.log({"loss": 0.5})
        run.flush()
        assert _payloads(server, "/log")[0]["config"] == {"lr": 0.1}

        run.config["lr"] = 0.05
        server["fail_next"] = True
        run.log({"loss": 0.4})
        run.flush()  # server returns 500: points are requeued, config change unconfirmed

        failed = _payloads(server, "/log")[1]
        assert failed["config"] == {"lr": 0.05}  # attempted, but not acknowledged

        run.flush()  # retries the requeued point; nothing new was logged
        retried = _payloads(server, "/log")[2]
        # Because the previous attempt never got a 2xx, the changed config
        # must still be considered unsent and go out again -- this is the
        # exact case that used to be lost.
        assert retried["config"] == {"lr": 0.05}


# -- the same contract via ThinkingFaceLightningLogger, against a real _Run ---


class _FakeLightningLoggerBase:
    """Stand-in for lightning.pytorch.loggers.logger.Logger."""


@pytest.fixture
def real_lightning_integration(monkeypatch):
    """Load thinkingface.trackio.integrations against a faked `lightning`
    package, but -- unlike tests/test_trackio_integrations.py -- without
    stubbing out `trackio.init`/`log`, so `ThinkingFaceLightningLogger`
    drives a real `_Run` and its real `flush()` retry/resend logic.
    """
    lightning_pkg = types.ModuleType("lightning")
    pytorch_pkg = types.ModuleType("lightning.pytorch")
    loggers_pkg = types.ModuleType("lightning.pytorch.loggers")
    logger_mod = types.ModuleType("lightning.pytorch.loggers.logger")
    logger_mod.Logger = _FakeLightningLoggerBase
    utilities_mod = types.ModuleType("lightning.pytorch.utilities")
    utilities_mod.rank_zero_only = lambda fn: fn

    lightning_pkg.pytorch = pytorch_pkg
    pytorch_pkg.loggers = loggers_pkg
    pytorch_pkg.utilities = utilities_mod
    loggers_pkg.logger = logger_mod

    fake_modules = {
        "lightning": lightning_pkg,
        "lightning.pytorch": pytorch_pkg,
        "lightning.pytorch.loggers": loggers_pkg,
        "lightning.pytorch.loggers.logger": logger_mod,
        "lightning.pytorch.utilities": utilities_mod,
    }
    for name, mod in fake_modules.items():
        monkeypatch.setitem(sys.modules, name, mod)

    import importlib

    # import_module rather than reload(sys.modules[...]): this file is a valid
    # target on its own, and then nothing has imported integrations yet.
    name = "thinkingface.trackio.integrations"
    mod = importlib.reload(importlib.import_module(name))
    assert mod._HAS_LIGHTNING is True
    yield mod

    for fake in fake_modules:
        del sys.modules[fake]
    importlib.reload(sys.modules[name])


class TestLightningLoggerResendsConfigThroughARealRun:
    def test_hyperparams_after_run_started_are_resent_on_next_flush(
        self, server, real_lightning_integration
    ):
        mod = real_lightning_integration
        logger = mod.ThinkingFaceLightningLogger(project="proj", name="r4")

        # log_metrics first: the run (and its first flush) starts before any
        # hyperparameter is known, exactly the ordering the bug depended on.
        logger.log_metrics({"loss": 0.9}, step=0)
        logger._run.flush()
        assert _payloads(server, "/log")[0]["config"] == {}

        logger.log_hyperparams({"batch_size": 32})
        logger.log_metrics({"loss": 0.8}, step=1)
        logger._run.flush()

        batches = _payloads(server, "/log")
        assert len(batches) == 2
        assert batches[1]["config"] == {"batch_size": 32}


class TestConfigThatCannotBePrepared:
    """A config value deepcopy refuses must not cost the run its points.

    The points are the part that cannot be reconstructed, and an exception out
    of flush() on the timer thread would also stop _schedule_flush from ever
    running again -- so the copy failure is reported and the config skipped.
    """

    def test_uncopyable_config_still_sends_the_metrics(self, server):
        run = trackio.init("proj", name="r1", config={"lr": 0.1})
        run.config["handle"] = threading.Lock()  # deepcopy refuses this

        run.log({"loss": 1.0})
        with pytest.warns(UserWarning, match="could not prepare the config"):
            run.flush()

        batches = _payloads(server, "/log")
        assert len(batches) == 1
        assert "config" not in batches[0]
        assert [p["metrics"]["loss"] for p in batches[0]["points"]] == [1.0]
        assert run._buffer == []
