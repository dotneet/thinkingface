"""Offline runs and the online mode's spill directory (docs/dev/agent-features.md §2.9).

The on-disk format is a contract with ``tf experiments sync`` (written in Go
against the same document), so these tests pin it record by record: the
directory name, ``init`` first, ``"v": 1`` everywhere, ``log.config`` only
when it changed, artifact copies under ``artifacts/``, and the ``finish``
record.

The spill half covers the online mode writing to disk the points it used to
drop -- a full retry buffer, and ``finish()`` running out of retries -- but
not points the server *rejected* (4xx), and falling back to the old
warn-and-drop when the disk refuses too.
"""

from __future__ import annotations

import json
import re
import sys
import types
import warnings
from pathlib import Path
from typing import Any

import pytest

import thinkingface.trackio as trackio
from thinkingface import _system_metrics
from thinkingface.trackio import _offline


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
def _isolated_env(monkeypatch, tmp_path):
    monkeypatch.setenv("THINKINGFACE_ENDPOINT", "http://localhost:8080")
    monkeypatch.delenv("THINKINGFACE_REPO", raising=False)
    monkeypatch.setenv("THINKINGFACE_META", "off")
    monkeypatch.setenv("THINKINGFACE_SYSTEM_METRICS", "off")
    monkeypatch.setenv("THINKINGFACE_OFFLINE_DIR", str(tmp_path / "off"))
    monkeypatch.delenv("THINKINGFACE_TOKEN", raising=False)
    monkeypatch.setattr(trackio, "_FLUSH_INTERVAL_SECONDS", 3600.0)
    monkeypatch.setattr(trackio, "_FINISH_FLUSH_BACKOFF_SECONDS", 0.001)
    monkeypatch.setattr(trackio, "_FINISH_FLUSH_MAX_BACKOFF_SECONDS", 0.002)
    yield
    run = trackio._current_run
    if run is not None:
        if run._timer is not None:
            run._timer.cancel()
        run._finished = True
        trackio._current_run = None


@pytest.fixture
def no_network(monkeypatch):
    calls: list[str] = []

    def refuse(url, *args, **kwargs):
        calls.append(url)
        raise AssertionError(f"offline mode made a request: {url}")

    monkeypatch.setattr(trackio.requests, "get", refuse)
    monkeypatch.setattr(trackio.requests, "post", refuse)
    monkeypatch.setattr(trackio.requests, "patch", refuse)
    return calls


def _records(run_dir: Path) -> list[dict[str, Any]]:
    lines = (run_dir / "run.jsonl").read_text(encoding="utf-8").splitlines()
    return [json.loads(line) for line in lines]


def _run_dirs(tmp_path: Path) -> list[Path]:
    base = tmp_path / "off"
    return sorted(p for p in base.iterdir()) if base.exists() else []


class TestDirectoryLayout:
    def test_slug(self):
        assert _offline.slug("my project/β v2") == "my_project___v2"
        assert len(_offline.slug("x" * 100)) == 40

    def test_name(self, tmp_path, no_network, capsys):
        trackio.init("my proj", name="run/1", mode="offline")
        (run_dir,) = _run_dirs(tmp_path)
        assert re.fullmatch(r"\d{8}T\d{6}-my_proj-run_1-[0-9a-f]{8}", run_dir.name)


class TestOfflineRun:
    def test_a_whole_run_makes_no_request_and_writes_the_contract_format(
        self, tmp_path, no_network, capsys
    ):
        artifact = tmp_path / "cm.png"
        artifact.write_bytes(b"png-bytes")

        run = trackio.init(
            "proj",
            name="r1",
            mode="offline",
            resume="allow",
            config={"lr": 0.1, "out": Path("o")},
            group="sweep",
            job_type="train",
        )
        run.log({"loss": 1.0})
        run.log({"loss": 0.5})
        run.flush()
        run.log({"loss": 0.25})
        run.flush()
        run.config["lr"] = 0.05
        run.log({"loss": 0.1})
        trackio.log_artifact(artifact, name="plots/cm.png")
        trackio.log_model("alice/bert")
        trackio.finish()

        assert no_network == []
        (run_dir,) = _run_dirs(tmp_path)
        records = _records(run_dir)
        assert all(r["v"] == 1 for r in records)
        assert [r["type"] for r in records] == [
            "init",
            "log",
            "log",
            "artifact",
            "model",
            "log",
            "finish",
        ]
        init = records[0]
        assert init["repo"] is None
        assert init["project"] == "proj"
        assert init["run"] == "r1"
        assert init["resume"] == "allow"
        assert init["group"] == "sweep"
        assert init["job_type"] == "train"
        assert init["config"] == {"lr": 0.1, "out": "o"}
        assert init["time"].endswith("Z")

        first, second, _, _, third = records[1:6]
        assert [p["step"] for p in first["points"]] == [0, 1]
        assert first["points"][0]["metrics"] == {"loss": 1.0}
        assert "timestamp" in first["points"][0]
        assert first["config"] == {"lr": 0.1, "out": "o"}  # the first log always carries it
        assert "config" not in second  # unchanged
        assert third["config"] == {"lr": 0.05, "out": "o"}  # changed

        assert records[3] == {
            "v": 1,
            "type": "artifact",
            "name": "plots/cm.png",
            "path": "artifacts/plots/cm.png",
        }
        assert (run_dir / "artifacts" / "plots" / "cm.png").read_bytes() == b"png-bytes"
        assert records[4] == {"v": 1, "type": "model", "repo_id": "alice/bert", "revision": ""}
        assert records[6]["status"] == "finished"

        err = capsys.readouterr().err
        assert str(run_dir) in err
        assert f"tf experiments sync {run_dir}" in err

    def test_repo_from_the_environment(self, tmp_path, no_network, monkeypatch, capsys):
        monkeypatch.setenv("THINKINGFACE_REPO", "alice/exp")
        trackio.init("proj", name="r2", mode="offline")
        (run_dir,) = _run_dirs(tmp_path)
        assert _records(run_dir)[0]["repo"] == "alice/exp"
        assert _records(run_dir)[0]["resume"] == "never"

    def test_env_selects_offline(self, tmp_path, no_network, monkeypatch, capsys):
        monkeypatch.setenv("THINKINGFACE_MODE", "offline")
        run = trackio.init("proj", name="r3", resume="must")  # no lookup, no raise
        assert run.offline
        run.finish(status="failed")
        (run_dir,) = _run_dirs(tmp_path)
        assert _records(run_dir)[-1]["status"] == "failed"

    def test_explicit_online_and_unknown_modes(self, monkeypatch):
        monkeypatch.setenv("THINKINGFACE_MODE", "offline")
        monkeypatch.setenv("THINKINGFACE_REPO", "alice/exp")
        monkeypatch.setattr(trackio.requests, "get", lambda *a, **k: _FakeResponse({"runs": []}))
        assert not trackio.init("proj", name="r4", mode="online").offline
        with pytest.raises(ValueError, match="mode"):
            trackio.init("proj", name="r5", mode="airplane")
        monkeypatch.setenv("THINKINGFACE_MODE", "airplane")
        with pytest.raises(ValueError, match="mode"):
            trackio.init("proj", name="r6")

    def test_a_hundred_points_are_written_without_waiting_for_the_timer(
        self, tmp_path, no_network, capsys
    ):
        run = trackio.init("proj", name="r7", mode="offline")
        for step in range(trackio._FLUSH_MAX_POINTS):
            run.log({"loss": float(step)})
        (run_dir,) = _run_dirs(tmp_path)
        logs = [r for r in _records(run_dir) if r["type"] == "log"]
        assert sum(len(r["points"]) for r in logs) == trackio._FLUSH_MAX_POINTS

    def test_system_metrics_are_written_too(self, tmp_path, no_network, monkeypatch, capsys):
        monkeypatch.setenv("THINKINGFACE_SYSTEM_METRICS", "on")
        monkeypatch.setattr(_system_metrics, "collect", lambda: {"system/cpu.percent": 12.0})
        run = trackio.init("proj", name="r8", mode="offline")
        run._on_timer()
        (run_dir,) = _run_dirs(tmp_path)
        (log,) = [r for r in _records(run_dir) if r["type"] == "log"]
        assert log["points"][0]["metrics"] == {"system/cpu.percent": 12.0}

    def test_an_unwritable_directory_warns_and_never_raises(
        self, tmp_path, no_network, monkeypatch
    ):
        blocker = tmp_path / "file"
        blocker.write_text("not a directory")
        monkeypatch.setenv("THINKINGFACE_OFFLINE_DIR", str(blocker))
        with pytest.warns(UserWarning, match="could not create a run directory"):
            run = trackio.init("proj", name="r9", mode="offline")
        run.log({"loss": 1.0})
        trackio.log_artifact(blocker)
        trackio.log_model("alice/m")
        run.finish()

    def test_unencodable_points_are_left_out_not_the_batch(self, tmp_path, no_network, capsys):
        run = trackio.init("proj", name="r10", mode="offline")
        run.log({"loss": 1.0})
        run._buffer.append({"step": 9, "timestamp": "t", "metrics": {"x": object()}})
        with pytest.warns(UserWarning, match="dropping 1 point"):
            run.flush()
        (run_dir,) = _run_dirs(tmp_path)
        (log,) = [r for r in _records(run_dir) if r["type"] == "log"]
        assert [p["step"] for p in log["points"]] == [0]

    def test_a_torn_last_line_does_not_hide_earlier_records(self, tmp_path, no_network, capsys):
        """The reader stops at a line that does not parse; everything before
        it must already be complete lines."""
        run = trackio.init("proj", name="r11", mode="offline")
        run.log({"loss": 1.0})
        run.flush()
        (run_dir,) = _run_dirs(tmp_path)
        text = (run_dir / "run.jsonl").read_text(encoding="utf-8")
        assert text.endswith("\n")
        assert all(json.loads(line) for line in text.splitlines())


# -- the online mode's spill directory ----------------------------------------


@pytest.fixture
def failing_server(monkeypatch):
    state: dict[str, Any] = {"posts": [], "log_status": 503}
    monkeypatch.setenv("THINKINGFACE_REPO", "alice/exp")

    def fake_get(url, headers=None, timeout=None):
        return _FakeResponse({"runs": []})

    def fake_post(url, json=None, headers=None, timeout=None):
        state["posts"].append((url, json))
        if url.endswith("/log"):
            return _FakeResponse(status_code=state["log_status"])
        return _FakeResponse({"ok": True})

    monkeypatch.setattr(trackio.requests, "get", fake_get)
    monkeypatch.setattr(trackio.requests, "post", fake_post)
    return state


class TestSpill:
    def test_finish_giving_up_writes_the_points_to_disk(self, tmp_path, failing_server):
        run = trackio.init("proj", name="r1", config={"lr": 0.1}, group="g")
        run.log({"loss": 1.0})
        run.log({"loss": 0.5})
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            run.finish()

        (run_dir,) = _run_dirs(tmp_path)
        messages = [str(w.message) for w in caught]
        assert any(
            "giving up" in m
            and "2 point(s)" in m
            and str(run_dir) in m
            and "tf experiments sync" in m
            for m in messages
        ), messages

        records = _records(run_dir)
        assert [r["type"] for r in records] == ["init", "log", "finish"]
        assert records[0]["resume"] == "allow"  # it continues a run the server has
        assert records[0]["repo"] == "alice/exp"
        assert records[0]["run"] == "r1"
        assert records[0]["group"] == "g"
        assert records[0]["config"] == {"lr": 0.1}
        assert [p["metrics"]["loss"] for p in records[1]["points"]] == [1.0, 0.5]
        assert records[1]["config"] == {"lr": 0.1}
        assert records[2]["status"] == "finished"
        assert run._buffer == []

    def test_a_full_retry_buffer_spills_its_oldest_points(
        self, tmp_path, failing_server, monkeypatch
    ):
        monkeypatch.setattr(trackio, "_BUFFER_MAX_POINTS", 3)
        run = trackio.init("proj", name="r2")
        run._buffer = [
            {"step": i, "timestamp": "t", "metrics": {"loss": float(i)}} for i in range(5)
        ]
        with pytest.warns(UserWarning, match="retry buffer full; wrote the 2 oldest"):
            run.flush()
        assert [p["step"] for p in run._buffer] == [2, 3, 4]

        (run_dir,) = _run_dirs(tmp_path)
        records = _records(run_dir)
        assert [r["type"] for r in records] == ["init", "log"]
        assert [p["step"] for p in records[1]["points"]] == [0, 1]

        # Later spills reuse the directory and only repeat a changed config.
        run._buffer += [{"step": 9, "timestamp": "t", "metrics": {"loss": 9.0}}]
        with pytest.warns(UserWarning, match="retry buffer full"):
            run._requeue([{"step": 8, "timestamp": "t", "metrics": {"loss": 8.0}}])
        records = _records(run_dir)
        assert [r["type"] for r in records] == ["init", "log", "log"]
        assert "config" not in records[2]

    def test_rejected_points_are_not_spilled(self, tmp_path, failing_server):
        failing_server["log_status"] = 400
        run = trackio.init("proj", name="r3")
        run.log({"loss": 1.0})
        with pytest.warns(UserWarning, match="dropping 1 point"):
            run.finish()
        assert _run_dirs(tmp_path) == []

    def test_nothing_is_written_when_everything_was_delivered(self, tmp_path, failing_server):
        failing_server["log_status"] = 200
        run = trackio.init("proj", name="r4")
        run.log({"loss": 1.0})
        run.finish()
        assert _run_dirs(tmp_path) == []

    def test_an_unwritable_disk_falls_back_to_dropping(self, tmp_path, failing_server, monkeypatch):
        blocker = tmp_path / "file"
        blocker.write_text("x")
        monkeypatch.setenv("THINKINGFACE_OFFLINE_DIR", str(blocker))
        run = trackio.init("proj", name="r5")
        run.log({"loss": 1.0})
        with pytest.warns(UserWarning, match="are being dropped"):
            run.finish()

    def test_artifacts_finish_could_not_commit_are_copied(
        self, tmp_path, failing_server, monkeypatch
    ):
        failing_server["log_status"] = 200

        class _Api:
            def __init__(self, endpoint=None, token=None):
                pass

            def create_commit(self, **kwargs):
                raise ConnectionError("bucket unreachable")

        stub = types.ModuleType("huggingface_hub")
        stub.CommitOperationAdd = lambda path_in_repo, path_or_fileobj: None
        stub.HfApi = _Api
        monkeypatch.setitem(sys.modules, "huggingface_hub", stub)

        f = tmp_path / "eval.json"
        f.write_text("{}")
        run = trackio.init("proj", name="r6")
        run.log_artifact(f)
        with pytest.warns(UserWarning, match="copies were written to"):
            run.finish()

        (run_dir,) = _run_dirs(tmp_path)
        records = _records(run_dir)
        assert [r["type"] for r in records] == ["init", "artifact", "finish"]
        assert records[1] == {
            "v": 1,
            "type": "artifact",
            "name": "eval.json",
            "path": "artifacts/eval.json",
        }
        assert (run_dir / "artifacts" / "eval.json").read_text() == "{}"


class TestSpillRepo:
    """A spill must never point `tf experiments sync` at the placeholder repo."""

    def test_an_unresolved_default_repo_is_written_as_null(self, tmp_path, monkeypatch):
        posts: list[str] = []

        def unreachable_get(url, headers=None, timeout=None):
            raise ConnectionError("server down")

        def unreachable_post(url, json=None, headers=None, timeout=None):
            posts.append(url)
            raise ConnectionError("server down")

        monkeypatch.setattr(trackio.requests, "get", unreachable_get)
        monkeypatch.setattr(trackio.requests, "post", unreachable_post)
        with pytest.warns(UserWarning, match="could not resolve the current user"):
            run = trackio.init("proj")  # THINKINGFACE_REPO unset, auto name
        # Online requests still go to the placeholder (and fail) ...
        assert run.repo == trackio._FALLBACK_REPO
        assert not run.repo_resolved
        run.log({"loss": 1.0})
        with pytest.warns(UserWarning, match="giving up"):
            run.finish()
        assert posts and all("/unknown/trackio-metrics/" in url for url in posts)

        # ... but the spill leaves the repository to the syncer.
        (run_dir,) = _run_dirs(tmp_path)
        records = _records(run_dir)
        assert records[0]["type"] == "init"
        assert "repo" in records[0] and records[0]["repo"] is None
        assert [r["type"] for r in records] == ["init", "log", "finish"]

    def test_a_resolved_default_repo_is_written_as_is(self, tmp_path, monkeypatch):
        def me(url, headers=None, timeout=None):
            assert url.endswith("/api/v1/me")
            return _FakeResponse({"user": {"username": "alice"}})

        def unreachable_post(url, json=None, headers=None, timeout=None):
            raise ConnectionError("server down")

        monkeypatch.setattr(trackio.requests, "get", me)
        monkeypatch.setattr(trackio.requests, "post", unreachable_post)
        run = trackio.init("proj")
        assert run.repo_resolved
        run.log({"loss": 1.0})
        with pytest.warns(UserWarning, match="giving up"):
            run.finish()
        (run_dir,) = _run_dirs(tmp_path)
        assert _records(run_dir)[0]["repo"] == "alice/trackio-metrics"


class TestSpillConfig:
    """A spill must not roll back a config the server already has."""

    def test_a_delivered_config_is_left_out(self, tmp_path, failing_server):
        failing_server["log_status"] = 200
        run = trackio.init("proj", name="r1", config={"lr": 0.1})
        run.log({"loss": 1.0})
        run.flush()  # delivers the config along with the first point
        assert failing_server["posts"][-1][1]["config"] == {"lr": 0.1}

        failing_server["log_status"] = 503
        run.log({"loss": 0.5})
        with pytest.warns(UserWarning, match="giving up"):
            run.finish()

        (run_dir,) = _run_dirs(tmp_path)
        records = _records(run_dir)
        assert [r["type"] for r in records] == ["init", "log", "finish"]
        assert "config" in records[0] and records[0]["config"] is None
        assert "config" not in records[1]
        assert [p["metrics"] for p in records[1]["points"]] == [{"loss": 0.5}]

    def test_a_config_changed_after_delivery_is_included(self, tmp_path, failing_server):
        failing_server["log_status"] = 200
        run = trackio.init("proj", name="r2", config={"lr": 0.1})
        run.log({"loss": 1.0})
        run.flush()

        failing_server["log_status"] = 503
        run.config["lr"] = 0.05  # never reached the server
        run.log({"loss": 0.5})
        with pytest.warns(UserWarning, match="giving up"):
            run.finish()

        (run_dir,) = _run_dirs(tmp_path)
        records = _records(run_dir)
        assert records[0]["config"] == {"lr": 0.05}
        assert records[1]["config"] == {"lr": 0.05}

    def test_a_config_never_delivered_is_included(self, tmp_path, failing_server):
        run = trackio.init("proj", name="r3", config={"lr": 0.1})
        run.log({"loss": 1.0})
        with pytest.warns(UserWarning, match="giving up"):
            run.finish()

        (run_dir,) = _run_dirs(tmp_path)
        records = _records(run_dir)
        assert records[0]["config"] == {"lr": 0.1}
        assert records[1]["config"] == {"lr": 0.1}


class TestNothingIsLoggedOnceFinishHasStarted:
    """log() and system-metrics sampling during finish() are refused, not lost."""

    def test_online_the_finish_window_refuses_points_media_and_system_metrics(
        self, tmp_path, failing_server, monkeypatch
    ):
        failing_server["log_status"] = 200
        monkeypatch.setenv("THINKINGFACE_SYSTEM_METRICS", "on")
        monkeypatch.setattr(_system_metrics, "collect", lambda: {"system/cpu.percent": 1.0})
        image = tmp_path / "img.png"
        image.write_bytes(b"png")
        run = trackio.init("proj", name="r1")
        staged: list[Any] = []
        monkeypatch.setattr(run, "_stage_media", lambda media, step: staged.append(media))
        run.log({"loss": 1.0})

        drain = run._drain_for_finish
        caught: list[warnings.WarningMessage] = []

        def drain_then_log():
            drain()
            # Between the final drain and _stop_background(): nothing will
            # ever flush this buffer again.
            with warnings.catch_warnings(record=True) as seen:
                warnings.simplefilter("always")
                run.log({"late": 2.0, "img": trackio.Image(image)})
                run._maybe_collect_system_metrics()
                run._log_system_metrics({"system/cpu.percent": 3.0})
            caught.extend(seen)

        monkeypatch.setattr(run, "_drain_for_finish", drain_then_log)
        run.finish()

        assert [str(w.message) for w in caught] == [
            "thinkingface.trackio: log() called after finish(); ignoring."
        ]
        assert run._buffer == []
        assert staged == []
        urls = [url for url, _ in failing_server["posts"]]
        assert urls[-1].endswith("/finish")
        sent = [
            p
            for url, body in failing_server["posts"]
            if url.endswith("/log")
            for p in body["points"]
        ]
        assert [p["metrics"] for p in sent] == [{"loss": 1.0}]
        assert _run_dirs(tmp_path) == []

    def test_offline_nothing_lands_after_the_finish_record(
        self, tmp_path, no_network, monkeypatch, capsys
    ):
        monkeypatch.setenv("THINKINGFACE_SYSTEM_METRICS", "on")
        monkeypatch.setattr(_system_metrics, "collect", lambda: {"system/cpu.percent": 1.0})
        run = trackio.init("proj", name="r2", mode="offline")
        run.log({"loss": 1.0})

        stop = run._stop_background

        def log_then_stop():
            with pytest.warns(UserWarning, match=r"log\(\) called after finish\(\)"):
                run.log({"late": 2.0})
            run._maybe_collect_system_metrics()
            stop()
            run.flush()  # a stray timer tick after finish()

        monkeypatch.setattr(run, "_stop_background", log_then_stop)
        run.finish()

        (run_dir,) = _run_dirs(tmp_path)
        records = _records(run_dir)
        assert [r["type"] for r in records] == ["init", "log", "finish"]
        assert [p["metrics"] for p in records[1]["points"]] == [{"loss": 1.0}]

    def test_media_staged_after_the_final_commit_is_refused(
        self, tmp_path, failing_server, monkeypatch
    ):
        failing_server["log_status"] = 200
        image = tmp_path / "img.png"
        image.write_bytes(b"png")
        run = trackio.init("proj", name="r3")
        upload = run._upload_artifacts

        def upload_then_stage(final=True):
            upload(final=final)
            # A log() that passed its check before finish() began, still
            # writing its media when the last commit has already happened.
            with pytest.warns(UserWarning, match="finished while it was being written"):
                run._stage_media({"img": trackio.Image(image)}, 7)
            with pytest.warns(UserWarning, match=r"log_artifact\(\) called after finish"):
                run._finishing = False  # get past log_artifact()'s own early check
                try:
                    run.log_artifact(image)
                finally:
                    run._finishing = True

        monkeypatch.setattr(run, "_upload_artifacts", upload_then_stage)
        run.finish()
        assert run._artifacts == []


class TestOfflineResumeSteps:
    """Offline, the server's last_step is unknown: say so once."""

    @pytest.mark.parametrize("resume", ["allow", "must", True])
    def test_auto_numbered_steps_warn_once(self, resume, tmp_path, no_network, capsys):
        run = trackio.init("proj", name="r1", mode="offline", resume=resume)
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            run.log({"loss": 1.0})
            run.log({"loss": 0.5})
        messages = [str(w.message) for w in caught]
        assert len(messages) == 1, messages
        assert "last_step" in messages[0] and "step=" in messages[0]

    def test_explicit_steps_do_not_warn(self, tmp_path, no_network, capsys):
        run = trackio.init("proj", name="r2", mode="offline", resume="allow")
        with warnings.catch_warnings():
            warnings.simplefilter("error")
            run.log({"loss": 1.0}, step=120)
            run.log({"loss": 0.5}, step=121)

    def test_resume_never_does_not_warn(self, tmp_path, no_network, capsys):
        run = trackio.init("proj", name="r3", mode="offline")
        with warnings.catch_warnings():
            warnings.simplefilter("error")
            run.log({"loss": 1.0})

    def test_online_resume_does_not_warn(self, monkeypatch):
        monkeypatch.setenv("THINKINGFACE_REPO", "alice/exp")
        monkeypatch.setattr(
            trackio.requests,
            "get",
            lambda *a, **k: _FakeResponse({"runs": [{"name": "r4", "last_step": 9}]}),
        )
        run = trackio.init("proj", name="r4", resume="allow")
        assert run.step == 10
        with warnings.catch_warnings():
            warnings.simplefilter("error")
            run.log({"loss": 1.0})
