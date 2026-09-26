"""Regression tests for finish() losing points and racing a live flush.

Two related bugs in ``_Run.finish()``:

1. ``finish()`` called ``flush()`` exactly once. If that flush hit a
   retryable failure (5xx / network), ``_send``/``_requeue`` put the points
   back in the buffer and warned "will retry on next flush" -- but
   ``finish()`` then set ``_finished`` and cancelled the timer anyway, so
   there never was a next flush. The final batch of a run sat in a buffer no
   one would ever look at again, and the ``atexit`` hook does nothing once
   ``_finished`` is already ``True``. ``finish()`` now retries its own final
   flush a few times with a short backoff, and only *then* warns (with an
   accurate count) that it is giving up.

2. ``finish()`` did not wait for a flush the background timer thread might
   already be in the middle of sending. If that in-flight ``POST /log``
   arrived at the server after ``finish()``'s own ``POST /finish``, the
   ingest upsert (which writes ``"status": "running"`` from every ``/log``
   unconditionally) could flip a just-finished run back to "running".
   ``flush()`` and ``finish()`` now serialize their network calls through a
   shared lock, so ``finish()`` cannot post ``/finish`` while another send for
   the same run is still in flight.

No server is involved: ``requests.get`` / ``requests.post`` are stubbed.
"""

from __future__ import annotations

import threading
import time
import warnings
from typing import Any

import pytest

import thinkingface.trackio as trackio


class _FakeResponse:
    def __init__(self, payload: Any = None, status_code: int = 200, text: str = "") -> None:
        self._payload = payload if payload is not None else {}
        self.status_code = status_code
        self.ok = 200 <= status_code < 300
        self.text = text

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
    monkeypatch.setattr(trackio, "_FINISH_FLUSH_BACKOFF_SECONDS", 0.01)
    monkeypatch.setattr(trackio, "_FINISH_FLUSH_MAX_BACKOFF_SECONDS", 0.02)
    monkeypatch.setattr(trackio.requests, "get", lambda *a, **k: _FakeResponse({"runs": []}))
    yield
    run = trackio._current_run
    if run is not None:
        if run._timer is not None:
            run._timer.cancel()
        run._finished = True
        trackio._current_run = None


def _point(step: int) -> dict[str, Any]:
    return {"step": step, "timestamp": "2026-01-01T00:00:00.000Z", "metrics": {"loss": float(step)}}


class TestFinishRetriesItsFinalFlush:
    def test_recovers_if_a_retry_succeeds_before_the_budget_runs_out(self, monkeypatch):
        calls: list[str] = []

        def flaky_post(url, json=None, headers=None, timeout=None):
            calls.append(url)
            if url.endswith("/log") and calls.count(url) <= 2:
                return _FakeResponse(status_code=500)
            return _FakeResponse({"ok": True})

        monkeypatch.setattr(trackio.requests, "post", flaky_post)
        run = trackio.init("proj", name="r1")
        run._buffer = [_point(0)]

        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            run.finish()

        messages = [str(w.message) for w in caught]
        assert not any("giving up" in m for m in messages), messages
        assert run._buffer == []
        log_calls = [c for c in calls if c.endswith("/log")]
        assert len(log_calls) == 3  # two failures, then success
        assert calls[-1].endswith("/finish")  # /finish still went out afterwards

    def test_gives_up_and_warns_with_an_accurate_count_after_the_budget_runs_out(self, monkeypatch):
        monkeypatch.setattr(trackio, "_FINISH_FLUSH_ATTEMPTS", 2)
        finish_posted = []

        def always_fail_log(url, json=None, headers=None, timeout=None):
            if url.endswith("/log"):
                return _FakeResponse(status_code=500)
            finish_posted.append(json)
            return _FakeResponse({"ok": True})

        monkeypatch.setattr(trackio.requests, "post", always_fail_log)
        run = trackio.init("proj", name="r2")
        run._buffer = [_point(0), _point(1)]

        with pytest.warns(UserWarning, match=r"giving up after 2 attempt\(s\).*2 point\(s\)"):
            run.finish()

        # Not left in the buffer for a trailing timer tick to send late and
        # make the warning a lie.
        assert run._buffer == []
        # finish() still records the run as finished even though its last
        # batch of points could not be delivered.
        assert len(finish_posted) == 1
        assert finish_posted[0]["status"] == "finished"

    def test_no_warning_or_retry_when_the_buffer_is_already_empty(self, monkeypatch):
        posts = []
        monkeypatch.setattr(
            trackio.requests,
            "post",
            lambda url, json=None, headers=None, timeout=None: (
                posts.append(url) or _FakeResponse({"ok": True})
            ),
        )
        run = trackio.init("proj", name="r3")

        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            run.finish()

        assert [str(w.message) for w in caught] == []
        assert posts == [f"{run._finish_url}"]  # no /log call at all


class TestFinishWaitsForAnInFlightFlush:
    def test_finish_does_not_post_finish_before_an_in_flight_log_completes(self, monkeypatch):
        hold = threading.Event()
        order: list[str] = []

        def fake_post(url, json=None, headers=None, timeout=None):
            if url.endswith("/log"):
                hold.wait(2.0)
                order.append("log")
            else:
                order.append("finish")
            return _FakeResponse({"ok": True})

        monkeypatch.setattr(trackio.requests, "post", fake_post)
        run = trackio.init("proj", name="r4")
        run._buffer = [_point(0)]

        flushing = threading.Thread(target=run.flush)
        flushing.start()
        # Give the background flush a chance to drain the buffer and block
        # inside fake_post while holding _send_lock, the way a timer-driven
        # flush that started just before finish() would.
        for _ in range(100):
            if not run._buffer:
                break
            time.sleep(0.01)
        assert run._buffer == []

        def release_soon():
            time.sleep(0.1)
            hold.set()

        threading.Thread(target=release_soon).start()
        run.finish()
        flushing.join(2.0)

        assert order == ["log", "finish"]

    def test_finish_retries_points_an_in_flight_flush_failed_to_send(self, monkeypatch):
        """The timer's flush has already drained the buffer and is mid-POST
        when finish() starts. When that POST fails, its points are requeued --
        and finish() must still send them rather than seeing an empty buffer,
        posting /finish, and stranding them."""
        hold = threading.Event()
        calls: list[tuple[str, int]] = []
        first_log = [True]

        def fake_post(url, json=None, headers=None, timeout=None):
            if url.endswith("/log"):
                if first_log[0]:
                    first_log[0] = False
                    hold.wait(2.0)
                    calls.append(("log-503", len(json["points"])))
                    return _FakeResponse(status_code=503)
                calls.append(("log", len(json["points"])))
            else:
                calls.append(("finish", 0))
            return _FakeResponse({"ok": True})

        monkeypatch.setattr(trackio.requests, "post", fake_post)
        run = trackio.init("proj", name="r5")
        run._buffer = [_point(0), _point(1)]

        flushing = threading.Thread(target=run.flush)
        flushing.start()
        for _ in range(100):
            if not run._buffer:
                break
            time.sleep(0.01)
        assert run._buffer == []

        def release_soon():
            time.sleep(0.1)
            hold.set()

        threading.Thread(target=release_soon).start()
        with warnings.catch_warnings():
            warnings.simplefilter("ignore")
            run.finish()
        flushing.join(2.0)

        assert calls == [("log-503", 2), ("log", 2), ("finish", 0)]
        assert run._buffer == []
