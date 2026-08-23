"""Tests for ``thinkingface.trackio.init(group=..., job_type=...)``.

The contract under test (see todo/exp-run-grouping.md) is that a
hyperparameter sweep can say which sweep each of its runs belongs to, so the
run table can fold the sweep into one row instead of listing forty:

* ``group=`` / ``job_type=`` reach the ingest API on every batch, not just
  the first one (the server keeps the stored value when a batch omits them,
  so repeating them is what makes a run created by an earlier attempt still
  land in the right group);
* they also ride the ``finish`` call, because a run that logged no points at
  all is created there;
* omitting them keeps the payload exactly as it was -- an ungrouped run is
  the backwards-compatible default, not a group named "";
* a value the server would reject (control characters, 256+ bytes) is
  rejected at the call site instead, where the traceback still points at it.

No server is involved: ``requests.get`` / ``requests.post`` are stubbed.
"""

from __future__ import annotations

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
    """A stand-in for the experiment API: an empty run table + a request log."""

    state: dict[str, Any] = {"runs": [], "posts": []}

    def fake_get(url, headers=None, timeout=None):
        return _FakeResponse({"runs": state["runs"]})

    def fake_post(url, json=None, headers=None, timeout=None):
        state["posts"].append((url, json))
        return _FakeResponse({"ok": True})

    monkeypatch.setattr(trackio.requests, "get", fake_get)
    monkeypatch.setattr(trackio.requests, "post", fake_post)
    return state


def _payloads(state, suffix: str) -> list[dict[str, Any]]:
    return [body for url, body in state["posts"] if url.endswith(suffix)]


class TestNormalizeGrouping:
    @pytest.mark.parametrize(
        ("value", "expected"),
        [(None, ""), ("", ""), ("   ", ""), ("lr-sweep", "lr-sweep"), ("  train  ", "train")],
    )
    def test_accepted_values(self, value, expected):
        assert trackio._normalize_grouping("group", value) == expected

    def test_control_characters_are_rejected(self):
        with pytest.raises(ValueError, match="control characters"):
            trackio._normalize_grouping("group", "lr\nsweep")

    def test_oversized_value_is_rejected(self):
        with pytest.raises(ValueError, match="at most"):
            trackio._normalize_grouping("group", "x" * 257)

    def test_non_string_is_rejected(self):
        with pytest.raises(TypeError, match="must be a string"):
            trackio._normalize_grouping("job_type", 3)


class TestGroupingIsSent:
    def test_every_batch_carries_the_grouping(self, server):
        run = trackio.init("proj", name="lr-0.1", group="lr-sweep", job_type="train")
        assert run.group == "lr-sweep"
        assert run.job_type == "train"

        run.log({"loss": 0.5})
        run.flush()
        run.log({"loss": 0.4})
        run.flush()

        batches = _payloads(server, "/log")
        assert len(batches) == 2
        for body in batches:
            assert body["group"] == "lr-sweep"
            assert body["job_type"] == "train"

    def test_finish_carries_the_grouping_too(self, server):
        """A run that logged nothing is created by the finish call, so the
        grouping has to ride along or the run falls out of its sweep."""
        trackio.init("proj", name="eval-only", group="lr-sweep", job_type="eval")
        trackio.finish()

        finishes = _payloads(server, "/finish")
        assert len(finishes) == 1
        assert finishes[0]["group"] == "lr-sweep"
        assert finishes[0]["job_type"] == "eval"

    def test_ungrouped_run_sends_no_grouping_keys(self, server):
        run = trackio.init("proj", name="solo")
        assert run.group == ""
        run.log({"loss": 0.5})
        run.flush()
        trackio.finish()

        for body in _payloads(server, "/log") + _payloads(server, "/finish"):
            assert "group" not in body
            assert "job_type" not in body

    def test_grouping_survives_a_resume(self, server):
        server["runs"] = [{"name": "lr-0.1", "last_step": 40, "config": {"lr": 0.1}}]
        run = trackio.init(
            "proj", name="lr-0.1", resume="allow", group="lr-sweep", job_type="train"
        )
        assert run.resumed is True
        run.log({"loss": 0.3})
        run.flush()
        assert _payloads(server, "/log")[0]["group"] == "lr-sweep"

    def test_bad_value_raises_at_the_call_site(self, server):
        with pytest.raises(ValueError, match="control characters"):
            trackio.init("proj", name="run", group="lr\tsweep")
