"""Tests for ``thinkingface.trackio.init(resume=...)``.

The contract under test (see todo/exp-run-resume.md) is that a run
interrupted on a Spot / preemptible VM and started again under the same
name charts as one continuous line:

* ``resume="allow"`` continues an existing run, ``"must"`` insists on one,
  and ``"never"`` (the default) never writes into somebody else's run --
  it renames instead, so this shim can never be the thing that aborts a
  training script;
* continuing means the step counter picks up at ``last_step + 1`` rather
  than restarting at 0 (which is what would draw the second attempt on top
  of the first), the run goes back to ``running``, and the two attempts'
  configs are merged with the newer values winning and the differences
  recorded.

No server is involved: the two HTTP calls init() can make (``GET .../runs``
and the ``POST .../log`` that a flush performs) are stubbed out.
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
    """Deterministic environment: a fixed repo, no env metadata, no telemetry,
    and a flush timer long enough never to fire during a test."""
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
    """A stand-in for the experiment API: a run table plus a request log."""

    state: dict[str, Any] = {"runs": [], "gets": [], "posts": [], "get_error": None}

    def fake_get(url, headers=None, timeout=None):
        state["gets"].append(url)
        if state["get_error"] is not None:
            raise state["get_error"]
        return _FakeResponse({"runs": state["runs"]})

    def fake_post(url, json=None, headers=None, timeout=None):
        state["posts"].append((url, json))
        return _FakeResponse({"ok": True})

    monkeypatch.setattr(trackio.requests, "get", fake_get)
    monkeypatch.setattr(trackio.requests, "post", fake_post)
    return state


class TestNormalizeResume:
    @pytest.mark.parametrize(
        ("value", "expected"),
        [
            (None, "never"),
            (False, "never"),
            (True, "allow"),
            ("allow", "allow"),
            ("MUST", "must"),
            ("  never  ", "never"),
        ],
    )
    def test_accepted_spellings(self, value, expected):
        assert trackio._normalize_resume(value) == expected

    def test_unknown_value_is_rejected(self):
        with pytest.raises(ValueError, match="resume must be one of"):
            trackio._normalize_resume("auto")


class TestUniqueRunName:
    def test_free_name_is_kept(self):
        assert trackio._unique_run_name("run", {"other"}) == "run"

    def test_first_free_suffix_is_used(self):
        assert trackio._unique_run_name("run", {"run"}) == "run-1"
        assert trackio._unique_run_name("run", {"run", "run-1", "run-2"}) == "run-3"


class TestResumeNever:
    def test_new_name_is_used_as_is(self, server):
        run = trackio.init("proj", name="fresh")
        assert run.name == "fresh"
        assert run.step == 0
        assert run.resumed is False

    def test_existing_name_is_renamed_rather_than_reused(self, server):
        server["runs"] = [{"name": "train", "last_step": 40, "config": {}}]
        with pytest.warns(UserWarning, match="already exists"):
            run = trackio.init("proj", name="train")
        assert run.name == "train-1"
        # A renamed run is a new run: it must start from zero, not from the
        # other run's last step.
        assert run.step == 0
        assert run.resumed is False

    def test_auto_generated_name_makes_no_lookup_request(self, server):
        """The default path must stay usable with no server reachable, so an
        init() that cannot collide does not even ask."""
        trackio.init("proj")
        assert server["gets"] == []

    def test_unreachable_server_keeps_the_requested_name(self, server):
        server["get_error"] = OSError("connection refused")
        run = trackio.init("proj", name="train")
        assert run.name == "train"
        assert run.step == 0


class TestResumeAllow:
    def test_continues_step_numbering_from_last_step(self, server):
        server["runs"] = [{"name": "train", "last_step": 40, "config": {"lr": 0.1}}]
        run = trackio.init("proj", name="train", resume="allow")

        assert run.name == "train"
        assert run.resumed is True
        assert run.step == 41, "the next logged step must continue the chart, not overwrite it"

    def test_first_logged_point_lands_after_the_previous_last_step(self, server):
        server["runs"] = [{"name": "train", "last_step": 40, "config": {}}]
        trackio.init("proj", name="train", resume="allow")
        trackio.log({"loss": 0.5})

        assert trackio._current_run._buffer[0]["step"] == 41

    def test_missing_run_simply_starts_a_new_one(self, server):
        run = trackio.init("proj", name="train", resume="allow")
        assert run.name == "train"
        assert run.step == 0
        assert run.resumed is False

    def test_true_is_accepted_as_allow(self, server):
        server["runs"] = [{"name": "train", "last_step": 7, "config": {}}]
        run = trackio.init("proj", name="train", resume=True)
        assert run.resumed is True
        assert run.step == 8

    def test_lookup_failure_warns_and_starts_from_zero(self, server):
        server["get_error"] = OSError("connection refused")
        with pytest.warns(UserWarning, match="could not check"):
            run = trackio.init("proj", name="train", resume="allow")
        assert run.step == 0
        assert run.resumed is False

    def test_status_goes_back_to_running_on_the_first_flush(self, server):
        server["runs"] = [{"name": "train", "last_step": 40, "config": {}, "status": "finished"}]
        trackio.init("proj", name="train", resume="allow")
        trackio.log({"loss": 0.5})
        trackio._current_run.flush()

        _, payload = server["posts"][-1]
        assert payload["run"] == "train"
        assert payload["status"] == "running"


class TestResumeMust:
    def test_missing_run_raises(self, server):
        with pytest.raises(RuntimeError, match="does not.*exist"):
            trackio.init("proj", name="train", resume="must")

    def test_existing_run_is_resumed(self, server):
        server["runs"] = [{"name": "train", "last_step": 3, "config": {}}]
        run = trackio.init("proj", name="train", resume="must")
        assert run.resumed is True
        assert run.step == 4

    def test_unreachable_server_raises_rather_than_starting_over(self, server):
        server["get_error"] = OSError("connection refused")
        with pytest.raises(RuntimeError, match="could not be looked up"):
            trackio.init("proj", name="train", resume="must")


class TestConfigMerge:
    def test_previous_keys_survive_and_new_values_win(self, server):
        server["runs"] = [
            {"name": "train", "last_step": 10, "config": {"lr": 0.1, "seed": 7}},
        ]
        run = trackio.init("proj", name="train", resume="allow", config={"lr": 0.05})

        assert run.config["seed"] == 7, "a key only the previous attempt set must survive"
        assert run.config["lr"] == 0.05, "on a conflict the value of the running code wins"

    def test_conflicts_are_recorded_on_the_run(self, server):
        server["runs"] = [{"name": "train", "last_step": 10, "config": {"lr": 0.1, "seed": 7}}]
        run = trackio.init("proj", name="train", resume="allow", config={"lr": 0.05, "seed": 7})

        resume_meta = run.config["_resume"]
        assert resume_meta["count"] == 1
        assert resume_meta["from_step"] == 11
        assert resume_meta["config_changes"] == {"lr": {"from": 0.1, "to": 0.05}}

    def test_repeated_resumes_increment_the_counter(self, server):
        server["runs"] = [
            {
                "name": "train",
                "last_step": 10,
                "config": {"_resume": {"count": 2, "config_changes": {}}},
            }
        ]
        run = trackio.init("proj", name="train", resume="allow")
        assert run.config["_resume"]["count"] == 3

    def test_env_metadata_is_replaced_not_diffed(self, server, monkeypatch):
        """_meta describes the attempt that is running now (a new commit, a new
        host), so it is overwritten wholesale and never reported as a change."""
        monkeypatch.delenv("THINKINGFACE_META", raising=False)
        monkeypatch.setattr(trackio._env_meta, "collect", lambda: {"host": "new-box"})
        server["runs"] = [{"name": "train", "last_step": 1, "config": {"_meta": {"host": "old"}}}]

        run = trackio.init("proj", name="train", resume="allow")
        assert run.config["_meta"] == {"host": "new-box"}
        assert run.config["_resume"]["config_changes"] == {}

    def test_merge_helper_leaves_a_fresh_run_alone(self):
        merged = trackio._merge_resumed_config(None, {"lr": 0.1}, from_step=0)
        assert merged["lr"] == 0.1
        assert merged["_resume"]["count"] == 1
        assert merged["_resume"]["config_changes"] == {}
