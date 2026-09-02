"""Regression tests: a NaN or an infinite metric must not wedge a run.

``requests`` serialises ``json=`` with ``allow_nan=False``, so a single
``float("nan")`` or ``float("inf")`` in a batch raised ``InvalidJSONError``
*before* any socket was touched. That raise was caught by the blanket handler
meant for network faults, which answered "retry", so ``_send`` put the same
poisoned batch back at the front of the buffer and every five-second flush
re-raised it: no further point for that run ever reached the server, and the
oldest points were eventually pushed off the front of the retry buffer.

A ``float("inf")`` sentinel for a best-so-far loss, or a loss that diverges to
NaN, is an ordinary thing for a training loop to log -- and
``integrations._numeric_only`` explicitly keeps such values -- so the client
now drops the offending *value* (warning once per run) and treats any
serialisation failure as non-retryable.

No server is involved: ``requests.get`` / ``requests.post`` are stubbed, with
the POST stub reproducing exactly what ``requests`` does with an unencodable
body.
"""

from __future__ import annotations

import json
import warnings
from typing import Any

import pytest
import requests

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
    """A stand-in for the ingest API that encodes bodies the way requests does.

    ``requests.models.PreparedRequest.prepare_body`` calls
    ``json.dumps(json, allow_nan=False)`` and re-raises the resulting
    ``ValueError`` as ``InvalidJSONError``; a value ``json`` cannot encode at
    all comes out as a plain ``TypeError``. Both happen before the request is
    sent, which is the whole point of this suite, so the stub does the same.
    """

    state: dict[str, Any] = {"posts": []}

    def fake_get(url, headers=None, timeout=None):
        return _FakeResponse({"runs": []})

    def fake_post(url, json=None, headers=None, timeout=None):
        _encode(json)
        state["posts"].append((url, json))
        return _FakeResponse({"ok": True})

    monkeypatch.setattr(trackio.requests, "get", fake_get)
    monkeypatch.setattr(trackio.requests, "post", fake_post)
    return state


def _encode(body: Any) -> str:
    try:
        return json.dumps(body, allow_nan=False)
    except ValueError as exc:
        raise requests.exceptions.InvalidJSONError(exc) from exc


def _log_bodies(state) -> list[dict[str, Any]]:
    return [body for url, body in state["posts"] if url.endswith("/log")]


def _metrics(state) -> list[dict[str, Any]]:
    return [point["metrics"] for body in _log_bodies(state) for point in body["points"]]


class TestNonFiniteMetrics:
    @pytest.mark.parametrize("bad", [float("nan"), float("inf"), float("-inf")])
    def test_the_bad_value_is_dropped_and_the_rest_is_delivered(self, server, bad):
        run = trackio.init("proj", name="r1")
        with pytest.warns(UserWarning, match="non-finite"):
            run.log({"loss": bad, "accuracy": 0.5})
        run.flush()

        assert _metrics(server) == [{"accuracy": 0.5}]
        assert run._buffer == []

    def test_a_mixed_batch_keeps_every_finite_value(self, server):
        run = trackio.init("proj", name="r2")
        with pytest.warns(UserWarning, match="'nan_metric', 'pos_inf', 'neg_inf'"):
            run.log(
                {
                    "loss": 0.25,
                    "nan_metric": float("nan"),
                    "pos_inf": float("inf"),
                    "neg_inf": float("-inf"),
                    "step_time": 12,
                }
            )
        run.flush()

        assert _metrics(server) == [{"loss": 0.25, "step_time": 12}]

    def test_a_point_with_nothing_left_is_not_sent(self, server):
        run = trackio.init("proj", name="r3")
        with pytest.warns(UserWarning, match="non-finite"):
            run.log({"best_loss": float("inf")})
        run.flush()

        assert _log_bodies(server) == []
        assert run._buffer == []
        # The step still advanced, so the next point lands where it would have.
        run.log({"loss": 1.0})
        run.flush()
        assert [p["step"] for b in _log_bodies(server) for p in b["points"]] == [1]

    def test_only_one_warning_per_run(self, server):
        run = trackio.init("proj", name="r4")
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            for _ in range(50):
                run.log({"loss": float("nan"), "accuracy": 0.5})
        messages = [str(w.message) for w in caught if "non-finite" in str(w.message)]
        assert len(messages) == 1
        run.flush()
        assert _metrics(server) == [{"accuracy": 0.5}] * 50

    def test_a_nan_never_wedges_the_delivery_of_later_points(self, server):
        """The end-to-end shape of the bug, from log() to the buffer."""
        run = trackio.init("proj", name="r5")
        with pytest.warns(UserWarning, match="non-finite"):
            run.log({"loss": float("nan")})
        for step in range(3):
            run.log({"loss": float(step)})
        run.flush()

        assert run._buffer == []
        assert _metrics(server) == [{"loss": 0.0}, {"loss": 1.0}, {"loss": 2.0}]

    def test_a_non_finite_system_metric_is_dropped(self, server):
        run = trackio.init("proj", name="r6")
        run._log_system_metrics({"cpu.percent": float("nan"), "memory.rss_mb": 128.0})
        run.flush()
        assert _metrics(server) == [{"memory.rss_mb": 128.0}]
        assert run._metric_keys == {"memory.rss_mb"}


class TestUnencodableBatches:
    def test_a_batch_that_cannot_be_encoded_is_dropped_not_requeued(self, server):
        """Nothing was sent and nothing ever will be, so it must not come back.

        The point is injected straight into the buffer: log() strips the
        non-finite values it can see, and this is the belt-and-braces layer
        underneath it (a numpy scalar, an object with a broken __float__, ...).
        """
        run = trackio.init("proj", name="r7")
        run._buffer = [
            {"step": 0, "timestamp": "2026-01-01T00:00:00.000Z", "metrics": {"loss": float("nan")}}
        ]
        with pytest.warns(UserWarning, match="cannot be encoded"):
            run.flush()

        assert run._buffer == []
        assert _log_bodies(server) == []

        # And the run keeps working afterwards.
        run.log({"loss": 1.0})
        run.flush()
        assert _metrics(server) == [{"loss": 1.0}]

    def test_an_unencodable_config_does_not_take_the_points_down_with_it(self, server):
        run = trackio.init("proj", name="r8", config={"limit": float("inf")})
        run.log({"loss": 1.0})
        with pytest.warns(UserWarning, match="config for run 'r8' cannot be encoded"):
            run.flush()

        bodies = _log_bodies(server)
        assert len(bodies) == 1
        assert "config" not in bodies[0]
        assert bodies[0]["points"][0]["metrics"] == {"loss": 1.0}
        assert run._buffer == []

        # The unencodable config is not re-attempted on every later flush...
        run.log({"loss": 2.0})
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            run.flush()
        assert [str(w.message) for w in caught] == []
        assert "config" not in _log_bodies(server)[1]

        # ...but a config that is fixed afterwards is sent.
        run.config["limit"] = 10.0
        run.log({"loss": 3.0})
        run.flush()
        assert _log_bodies(server)[2]["config"] == {"limit": 10.0}

    def test_unencodable_points_do_not_mark_the_config_as_delivered(self, server):
        """The config is only "sent" once a call that carried it succeeded.

        The handler used to blame the config unconditionally: any encode error
        raised while a config was attached warned about the config, recorded it
        as delivered, and retried the points alone. When the *points* were the
        unencodable half that retry failed too, so nothing was transmitted at
        all -- and the config was now permanently marked as sent, so it never
        reached the server again until ``run.config`` was mutated. A numpy
        scalar reaches this path with no NaN in sight: it is finite, so
        ``_finite_metrics`` keeps it, and ``json`` cannot encode it.
        """
        run = trackio.init("proj", name="r9", config={"lr": 0.1})
        # Stands in for np.float32(0.5): finite, and not JSON-encodable.
        run._buffer = [
            {"step": 0, "timestamp": "2026-01-01T00:00:00.000Z", "metrics": {"loss": object()}}
        ]
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            run.flush()
        messages = [str(w.message) for w in caught]

        # Nothing went out, and the config was not blamed for it.
        assert _log_bodies(server) == []
        assert run._buffer == []
        assert any("dropping 1 point(s)" in m for m in messages)
        assert not any("config for run 'r9' cannot be encoded" in m for m in messages)

        # The config was never transmitted, so it is still pending and rides
        # out with the next batch that can be encoded.
        assert run._config_sent is False
        run.log({"loss": 1.0})
        run.flush()
        bodies = _log_bodies(server)
        assert len(bodies) == 1
        assert bodies[0]["config"] == {"lr": 0.1}
