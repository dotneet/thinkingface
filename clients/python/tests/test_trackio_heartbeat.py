"""Heartbeats (docs/dev/agent-features.md §2.6).

Every ``/log`` declares ``heartbeat_secs`` so the server can tell a run that
is merely quiet from one whose process died; and a run that has posted
nothing for that long sends ``{"points": []}`` from the flush timer as a
liveness ping. The ping goes through ``_send_lock`` like every other request
and never starts once ``finish()`` has.

No server is involved: ``requests`` is stubbed, and the clock is moved by
back-dating the run's bookkeeping rather than by sleeping.
"""

from __future__ import annotations

import threading
import warnings
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
    state: dict[str, Any] = {"posts": [], "status": 200, "raise": None}

    def fake_get(url, headers=None, timeout=None):
        return _FakeResponse({"runs": []})

    def fake_post(url, json=None, headers=None, timeout=None):
        state["posts"].append((url, json))
        if state["raise"] is not None:
            raise state["raise"]
        return _FakeResponse({"ok": True}, status_code=state["status"])

    monkeypatch.setattr(trackio.requests, "get", fake_get)
    monkeypatch.setattr(trackio.requests, "post", fake_post)
    return state


def _logs(state) -> list[dict[str, Any]]:
    return [body for url, body in state["posts"] if url.endswith("/log")]


def _go_quiet(run, seconds: float) -> None:
    """Pretend nothing has been posted (or attempted) for ``seconds``."""
    run._last_post_at -= seconds
    run._last_heartbeat_attempt_at -= seconds


class TestHeartbeatSecsOnEveryBatch:
    def test_default_is_30(self, server):
        run = trackio.init("proj", name="r1")
        run.log({"loss": 1.0})
        run.flush()
        assert _logs(server)[0]["heartbeat_secs"] == 30

    @pytest.mark.parametrize(("raw", "expected"), [("45", 45), ("7200", 3600), ("12.9", 12)])
    def test_env_override_is_clamped_to_what_the_server_accepts(
        self, server, monkeypatch, raw, expected
    ):
        monkeypatch.setenv("THINKINGFACE_HEARTBEAT_SECS", raw)
        run = trackio.init("proj", name="r2")
        run.log({"loss": 1.0})
        run.flush()
        assert _logs(server)[0]["heartbeat_secs"] == expected

    def test_zero_disables_the_field(self, server, monkeypatch):
        monkeypatch.setenv("THINKINGFACE_HEARTBEAT_SECS", "0")
        run = trackio.init("proj", name="r3")
        run.log({"loss": 1.0})
        run.flush()
        assert "heartbeat_secs" not in _logs(server)[0]

    def test_garbage_warns_and_uses_the_default(self, server, monkeypatch):
        monkeypatch.setenv("THINKINGFACE_HEARTBEAT_SECS", "soon")
        with pytest.warns(UserWarning, match="THINKINGFACE_HEARTBEAT_SECS"):
            run = trackio.init("proj", name="r4")
        assert run.heartbeat_secs == 30


class TestPing:
    def test_a_quiet_run_pings_from_the_timer(self, server):
        run = trackio.init("proj", name="r5", group="sweep", job_type="train", config={"a": 1})
        _go_quiet(run, 31)
        run._on_timer()

        (ping,) = _logs(server)
        assert ping == {
            "run": "r5",
            "status": "running",
            "points": [],
            "heartbeat_secs": 30,
            "group": "sweep",
            "job_type": "train",
        }
        # The config is not considered delivered by a ping: it still rides
        # the first real batch.
        run.log({"loss": 1.0})
        run.flush()
        assert _logs(server)[1]["config"] == {"a": 1}

    def test_no_ping_while_the_run_is_posting(self, server):
        run = trackio.init("proj", name="r6")
        _go_quiet(run, 31)
        run.log({"loss": 1.0})
        run._on_timer()  # the flush posts, so there is nothing to ping for

        assert [len(body["points"]) for body in _logs(server)] == [1]

    def test_no_ping_before_the_interval(self, server):
        run = trackio.init("proj", name="r7")
        _go_quiet(run, 10)
        run._on_timer()
        assert _logs(server) == []

    def test_one_ping_per_interval(self, server):
        run = trackio.init("proj", name="r8")
        _go_quiet(run, 31)
        run._on_timer()
        run._on_timer()
        assert len(_logs(server)) == 1

    def test_a_failed_ping_is_silent_and_not_retried_every_tick(self, server):
        server["raise"] = ConnectionError("down")
        run = trackio.init("proj", name="r9")
        _go_quiet(run, 31)
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            run._on_timer()
            run._on_timer()
        assert [str(w.message) for w in caught] == []
        assert len(_logs(server)) == 1

    def test_a_rejected_ping_does_not_count_as_a_post(self, server):
        server["status"] = 503
        run = trackio.init("proj", name="r10")
        _go_quiet(run, 31)
        run._on_timer()
        assert run._last_post_at < run._last_heartbeat_attempt_at

    def test_disabled(self, server, monkeypatch):
        monkeypatch.setenv("THINKINGFACE_HEARTBEAT_SECS", "0")
        run = trackio.init("proj", name="r11")
        _go_quiet(run, 10_000)
        run._on_timer()
        assert _logs(server) == []

    def test_never_once_finish_has_started(self, server):
        run = trackio.init("proj", name="r12")
        _go_quiet(run, 31)
        run._finishing = True
        run._maybe_heartbeat()
        assert _logs(server) == []

    def test_a_ping_in_flight_lands_before_finish(self, server, monkeypatch):
        """The ping holds _send_lock, so /finish waits for it."""
        hold = threading.Event()
        order: list[str] = []

        def fake_post(url, json=None, headers=None, timeout=None):
            if url.endswith("/log"):
                hold.wait(2.0)
                order.append("ping")
            else:
                order.append("finish")
            return _FakeResponse({"ok": True})

        monkeypatch.setattr(trackio.requests, "post", fake_post)
        run = trackio.init("proj", name="r13")
        _go_quiet(run, 31)
        pinging = threading.Thread(target=run._maybe_heartbeat)
        pinging.start()
        for _ in range(100):
            if run._send_lock.locked():
                break
            threading.Event().wait(0.01)
        threading.Timer(0.1, hold.set).start()
        run.finish()
        pinging.join(2.0)
        assert order == ["ping", "finish"]

    def test_offline_runs_never_ping(self, server, monkeypatch, capsys):
        run = trackio.init("proj", name="r14", mode="offline")
        _go_quiet(run, 31)
        run._on_timer()
        assert server["posts"] == []
