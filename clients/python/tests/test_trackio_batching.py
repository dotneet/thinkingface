"""Regression tests: a flush must never post more points than the server takes.

``_BUFFER_MAX_POINTS`` (the retry buffer's ceiling) and the server's
``maxIngestPoints`` were both 10,000, and ``flush()`` posted the whole buffer
as one request. So the ordinary sequence after a disconnection -- requeue
fills the buffer to exactly 10,000, the next ``log()`` adds one more and trips
the flush threshold -- sent 10,001 points, which the server answers with a 400
("a batch may carry at most 10000 points"). A 400 is not retryable, so all
10,001 points were dropped: an outage recovering was the thing that destroyed
the data it had been holding.

``flush()`` now splits whatever it drained into ``_MAX_POINTS_PER_REQUEST``
sized requests, so the two limits no longer have to line up, and a partial
failure only requeues the chunks that did not go out.

No server is involved: ``requests.get`` / ``requests.post`` are stubbed.
"""

from __future__ import annotations

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
    """A stand-in for the ingest API that enforces the real point ceiling.

    ``state["fail_posts"]`` is a set of 0-based POST indexes that answer with a
    retryable 500 instead, so a test can fail one chunk in the middle.
    """

    state: dict[str, Any] = {"posts": [], "fail_posts": set()}

    def fake_get(url, headers=None, timeout=None):
        return _FakeResponse({"runs": []})

    def fake_post(url, json=None, headers=None, timeout=None):
        index = len(state["posts"])
        state["posts"].append((url, json))
        if index in state["fail_posts"]:
            return _FakeResponse(status_code=500)
        points = (json or {}).get("points") or []
        # Exactly what backend/internal/api/experiments.go answers.
        if len(points) > trackio._MAX_POINTS_PER_REQUEST:
            return _FakeResponse(
                status_code=400,
                text=f"a batch may carry at most {trackio._MAX_POINTS_PER_REQUEST} points",
            )
        return _FakeResponse({"ok": True})

    monkeypatch.setattr(trackio.requests, "get", fake_get)
    monkeypatch.setattr(trackio.requests, "post", fake_post)
    return state


def _log_batches(state) -> list[dict[str, Any]]:
    return [body for url, body in state["posts"] if url.endswith("/log")]


def _point(step: int) -> dict[str, Any]:
    return {"step": step, "timestamp": "2026-01-01T00:00:00.000Z", "metrics": {"loss": float(step)}}


class TestFlushBatching:
    def test_full_buffer_plus_one_point_is_delivered(self, server):
        """The exact shape of the bug: a buffer filled to its ceiling by a
        requeue, plus one freshly logged point."""
        run = trackio.init("proj", name="r1")
        run._buffer = [_point(i) for i in range(trackio._BUFFER_MAX_POINTS)]
        run.log({"loss": 1.0})  # trips the flush threshold at 10,001 points

        batches = _log_batches(server)
        sizes = [len(b["points"]) for b in batches]
        assert all(size <= trackio._MAX_POINTS_PER_REQUEST for size in sizes), sizes
        assert sum(sizes) == trackio._BUFFER_MAX_POINTS + 1
        assert run._buffer == []

    def test_flush_splits_at_the_request_limit(self, server, monkeypatch):
        monkeypatch.setattr(trackio, "_MAX_POINTS_PER_REQUEST", 3)
        run = trackio.init("proj", name="r2")
        run._buffer = [_point(i) for i in range(7)]
        run.flush()

        sizes = [len(b["points"]) for b in _log_batches(server)]
        assert sizes == [3, 3, 1]
        steps = [p["step"] for b in _log_batches(server) for p in b["points"]]
        assert steps == list(range(7))  # order preserved across the split

    def test_a_failed_chunk_requeues_only_what_was_not_sent(self, server, monkeypatch):
        monkeypatch.setattr(trackio, "_MAX_POINTS_PER_REQUEST", 3)
        run = trackio.init("proj", name="r3")
        run._buffer = [_point(i) for i in range(7)]
        server["fail_posts"] = {1}  # the second chunk

        with pytest.warns(UserWarning, match="will retry"):
            run.flush()

        # Chunk 0 went out; chunks 1 and 2 are back in the buffer, in order.
        assert [p["step"] for p in run._buffer] == [3, 4, 5, 6]
        sizes = [len(b["points"]) for b in _log_batches(server)]
        assert sizes == [3, 3]  # the third chunk was never attempted

        server["fail_posts"] = set()
        run.flush()
        assert run._buffer == []
        steps = [p["step"] for b in _log_batches(server) for p in b["points"]]
        assert sorted(set(steps)) == list(range(7))

    def test_config_rides_only_the_first_accepted_chunk(self, server, monkeypatch):
        monkeypatch.setattr(trackio, "_MAX_POINTS_PER_REQUEST", 2)
        run = trackio.init("proj", name="r4", config={"lr": 0.1})
        run._buffer = [_point(i) for i in range(5)]
        run.flush()

        batches = _log_batches(server)
        assert len(batches) == 3
        assert batches[0]["config"] == {"lr": 0.1}
        assert all("config" not in b for b in batches[1:])

    def test_a_rejected_batch_drops_the_rest_with_a_warning(self, server, monkeypatch):
        """A 4xx is about the request, not the chunk, so the remaining points
        go too -- but the user is told how many, not left to infer it."""
        monkeypatch.setattr(trackio, "_MAX_POINTS_PER_REQUEST", 2)

        def reject(url, json=None, headers=None, timeout=None):
            server["posts"].append((url, json))
            return _FakeResponse(status_code=401, text="bad token")

        monkeypatch.setattr(trackio.requests, "post", reject)
        run = trackio.init("proj", name="r5")
        run._buffer = [_point(i) for i in range(5)]

        with pytest.warns(UserWarning) as caught:
            run.flush()

        messages = [str(w.message) for w in caught]
        assert any("dropping 2 point(s)" in m for m in messages), messages
        assert any("remaining 3 buffered point(s)" in m for m in messages), messages
        assert len(_log_batches(server)) == 1  # no pointless retries of the same 401
        assert run._buffer == []


def _messages(recorded) -> list[str]:
    return [str(w.message) for w in recorded]


class TestMetricKeyCeiling:
    def test_warns_once_when_a_run_exceeds_the_server_key_limit(self, server, monkeypatch):
        monkeypatch.setattr(trackio, "_MAX_METRIC_KEYS", 3)
        run = trackio.init("proj", name="r6")

        run.log({"a": 1.0, "b": 2.0, "c": 3.0})
        with pytest.warns(UserWarning, match="distinct metric names"):
            run.log({"d": 4.0})

        # A one-off: a run inventing a name every step must not turn into a
        # warning every step.
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            run.log({"e": 5.0})
        assert [m for m in _messages(caught) if "distinct metric names" in m] == []

    def test_a_fixed_set_of_names_never_warns(self, server, monkeypatch):
        monkeypatch.setattr(trackio, "_MAX_METRIC_KEYS", 3)
        run = trackio.init("proj", name="r7")
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            for step in range(50):
                run.log({"loss": float(step), "acc": 0.5})
        assert [m for m in _messages(caught) if "distinct metric names" in m] == []


class TestInitCompatibilityKwargs:
    def test_unknown_kwargs_are_reported(self, server):
        with pytest.warns(UserWarning, match="tags"):
            trackio.init("proj", name="r8", tags=["baseline", "v2"])

    def test_known_arguments_are_silent(self, server):
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            trackio.init("proj", name="r9", config={"lr": 0.1}, group="sweep", job_type="train")
        assert [m for m in _messages(caught) if "unsupported argument" in m] == []
