"""Tests for ``trackio.log_artifact`` and ``trackio.log_model``.

The contract under test (todo/exp-run-artifacts.md, todo/exp-run-model-link.md):

* an artifact goes to ``{project}/artifacts/{run}/{name}`` in the run's own
  dataset repository, through the ordinary HuggingFace commit path -- and a
  run's whole set of artifacts becomes *one* commit at ``finish()``, not one
  commit per file;
* a name that would climb out of the run's directory, or that the server's
  parquet indexer would mistake for a metrics table, is refused;
* ``log_model`` resolves the model's current HEAD when given no revision and
  records the link through the run *annotation* endpoint, which is the one
  path a re-index cannot erase.

No server and no huggingface_hub upload is involved: the HTTP calls and
``HfApi.create_commit`` are stubbed out.
"""

from __future__ import annotations

import sys
import types
from pathlib import Path
from typing import Any

import pytest

import thinkingface.trackio as trackio
from thinkingface.trackio import _artifacts


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
    """Stand-in for the experiment API plus the HF commit endpoint."""

    state: dict[str, Any] = {
        "runs": [],
        "gets": [],
        "posts": [],
        "patches": [],
        "commits": [],
        "sha": "a1b2c3d4",
        "get_error": None,
    }

    def fake_get(url, headers=None, timeout=None):
        state["gets"].append(url)
        if state["get_error"] is not None:
            raise state["get_error"]
        if "/api/models/" in url:
            return _FakeResponse({"sha": state["sha"]})
        return _FakeResponse({"runs": state["runs"]})

    def fake_post(url, json=None, headers=None, timeout=None):
        state["posts"].append((url, json))
        return _FakeResponse({"ok": True})

    def fake_patch(url, json=None, headers=None, timeout=None):
        state["patches"].append((url, json))
        return _FakeResponse({"run": {}})

    monkeypatch.setattr(trackio.requests, "get", fake_get)
    monkeypatch.setattr(trackio.requests, "post", fake_post)
    monkeypatch.setattr(trackio.requests, "patch", fake_patch)

    # huggingface_hub is a real dependency, but a commit must not be attempted
    # for real here; a stub module keeps the test independent of its version.
    class _Add:
        def __init__(self, path_in_repo, path_or_fileobj):
            self.path_in_repo = path_in_repo
            self.path_or_fileobj = path_or_fileobj

    class _Api:
        def __init__(self, endpoint=None, token=None):
            self.endpoint = endpoint

        def create_commit(self, repo_id, repo_type, operations, commit_message):
            state["commits"].append(
                {
                    "repo_id": repo_id,
                    "repo_type": repo_type,
                    "message": commit_message,
                    "paths": [op.path_in_repo for op in operations],
                    "sources": [op.path_or_fileobj for op in operations],
                }
            )

    stub = types.ModuleType("huggingface_hub")
    stub.CommitOperationAdd = _Add
    stub.HfApi = _Api
    monkeypatch.setitem(sys.modules, "huggingface_hub", stub)
    return state


class TestNormalizeArtifactName:
    @pytest.mark.parametrize(
        ("raw", "expected"),
        [
            ("confusion.png", "confusion.png"),
            ("  plots/roc.png  ", "plots/roc.png"),
            ("/leading/slash.txt", "leading/slash.txt"),
            ("plots\\win.png", "plots/win.png"),
            ("./a/./b.json", "a/b.json"),
        ],
    )
    def test_accepted(self, raw, expected):
        assert _artifacts.normalize_artifact_name(raw) == expected

    @pytest.mark.parametrize("raw", ["", "   ", "/", "../escape.txt", "a/../../b.txt"])
    def test_rejected(self, raw):
        with pytest.raises(ValueError):
            _artifacts.normalize_artifact_name(raw)

    def test_metrics_parquet_is_reserved(self):
        """The indexer reads {dir}/metrics.parquet as a project called {dir}
        (backend/internal/experiments/layout.go), so an artifact with that
        name would invent a project called "{project}/artifacts/{run}"."""
        with pytest.raises(ValueError, match="reserved"):
            _artifacts.normalize_artifact_name("metrics.parquet")
        # Only at the top of the artifact directory: a nested one is harmless.
        assert _artifacts.normalize_artifact_name("eval/metrics.parquet") == "eval/metrics.parquet"


class TestArtifactPath:
    def test_layout_is_the_documented_one(self):
        assert _artifacts.artifact_path("sentiment", "run-42", "cm.png") == (
            "sentiment/artifacts/run-42/cm.png"
        )


class TestStage:
    def test_file_defaults_to_its_basename(self, tmp_path):
        f = tmp_path / "confusion.png"
        f.write_bytes(b"x")
        assert _artifacts.stage(f) == [(f, "confusion.png")]

    def test_explicit_name_wins(self, tmp_path):
        f = tmp_path / "confusion.png"
        f.write_bytes(b"x")
        assert _artifacts.stage(f, "plots/cm.png") == [(f, "plots/cm.png")]

    def test_directory_keeps_its_layout(self, tmp_path):
        root = tmp_path / "samples"
        (root / "nested").mkdir(parents=True)
        (root / "a.txt").write_text("a")
        (root / "nested" / "b.txt").write_text("b")

        staged = _artifacts.stage(root)
        assert sorted(name for _, name in staged) == ["samples/a.txt", "samples/nested/b.txt"]

    def test_empty_directory_is_an_error(self, tmp_path):
        (tmp_path / "empty").mkdir()
        with pytest.raises(ValueError, match="no files"):
            _artifacts.stage(tmp_path / "empty")

    def test_missing_path_is_an_error(self, tmp_path):
        with pytest.raises(ValueError, match="not a file or directory"):
            _artifacts.stage(tmp_path / "nope.txt")

    def test_too_many_files_is_an_error(self, tmp_path, monkeypatch):
        monkeypatch.setattr(_artifacts, "MAX_FILES_PER_ARTIFACT", 2)
        root = tmp_path / "many"
        root.mkdir()
        for i in range(3):
            (root / f"{i}.txt").write_text("x")
        with pytest.raises(ValueError, match="more than the 2"):
            _artifacts.stage(root)

    def test_over_the_limit_says_that_nothing_is_uploaded(self, tmp_path, monkeypatch):
        """The message has to be unmistakable: this is not a truncation to the
        first N files, it is a refusal to upload any of them -- which is how a
        sharded checkpoint directory ended up silently absent."""
        monkeypatch.setattr(_artifacts, "MAX_FILES_PER_ARTIFACT", 2)
        root = tmp_path / "checkpoint"
        root.mkdir()
        for i in range(3):
            (root / f"shard-{i}.safetensors").write_text("x")
        with pytest.raises(ValueError, match="none of them are uploaded"):
            _artifacts.stage(root)

    def test_symlinked_files_are_followed(self, tmp_path):
        """The documented behaviour ("symlinks are followed for files"), which
        the implementation used to contradict by skipping every symlink."""
        real = tmp_path / "real.bin"
        real.write_bytes(b"weights")
        root = tmp_path / "ckpt"
        root.mkdir()
        (root / "plain.txt").write_text("x")
        (root / "linked.bin").symlink_to(real)

        staged = _artifacts.stage(root)
        assert sorted(name for _, name in staged) == ["ckpt/linked.bin", "ckpt/plain.txt"]

    def test_symlinked_directories_and_dangling_links_are_skipped_loudly(self, tmp_path):
        other = tmp_path / "other"
        other.mkdir()
        (other / "deep.txt").write_text("x")
        root = tmp_path / "ckpt"
        root.mkdir()
        (root / "plain.txt").write_text("x")
        (root / "loop").symlink_to(other, target_is_directory=True)
        (root / "dangling.bin").symlink_to(tmp_path / "gone.bin")

        with pytest.warns(UserWarning, match="not uploading 2 symlink"):
            staged = _artifacts.stage(root)
        assert [name for _, name in staged] == ["ckpt/plain.txt"]


class TestLogArtifact:
    def test_nothing_is_uploaded_before_finish(self, server, tmp_path):
        f = tmp_path / "cm.png"
        f.write_bytes(b"x")
        trackio.init("proj", name="run-1")
        trackio.log_artifact(f)
        assert server["commits"] == []

    def test_every_artifact_lands_in_one_commit(self, server, tmp_path):
        for name in ("a.png", "b.png", "c.json"):
            (tmp_path / name).write_text("x")
        trackio.init("proj", name="run-1")
        for name in ("a.png", "b.png", "c.json"):
            trackio.log_artifact(tmp_path / name)
        trackio.finish()

        assert len(server["commits"]) == 1, "one run must not make one commit per artifact"
        commit = server["commits"][0]
        assert commit["repo_id"] == "alice/exp"
        assert commit["repo_type"] == "dataset"
        assert commit["paths"] == [
            "proj/artifacts/run-1/a.png",
            "proj/artifacts/run-1/b.png",
            "proj/artifacts/run-1/c.json",
        ]

    def test_no_artifacts_means_no_commit(self, server):
        trackio.init("proj", name="run-1")
        trackio.finish()
        assert server["commits"] == []

    def test_bad_name_warns_and_uploads_nothing(self, server, tmp_path):
        f = tmp_path / "cm.png"
        f.write_bytes(b"x")
        trackio.init("proj", name="run-1")
        with pytest.warns(UserWarning, match="uploaded nothing"):
            trackio.log_artifact(f, name="../escape.png")
        trackio.finish()
        assert server["commits"] == []

    def test_upload_failure_never_raises(self, server, tmp_path, monkeypatch):
        f = tmp_path / "cm.png"
        f.write_bytes(b"x")
        trackio.init("proj", name="run-1")
        trackio.log_artifact(f)

        def boom(*args, **kwargs):
            raise RuntimeError("bucket on fire")

        monkeypatch.setattr(sys.modules["huggingface_hub"].HfApi, "create_commit", boom)
        with pytest.warns(UserWarning, match="failed to upload"):
            trackio.finish()

    def test_before_init_is_a_warning(self, server, tmp_path):
        trackio._current_run = None
        with pytest.warns(UserWarning, match="before init"):
            trackio.log_artifact(tmp_path)


class TestLogModel:
    def test_head_is_resolved_when_no_revision_is_given(self, server):
        trackio.init("proj", name="run-1")
        trackio.log_model("alice/bert-ja")
        trackio.finish()

        assert any("/api/models/alice/bert-ja" in url for url in server["gets"])
        (url, body), *rest = server["patches"]
        assert not rest
        assert url == "http://localhost:8080/api/v1/experiments/alice/exp/proj/runs/run-1"
        assert body == {"models": [{"repo_id": "alice/bert-ja", "revision": "a1b2c3d4"}]}

    def test_explicit_revision_is_not_looked_up(self, server):
        trackio.init("proj", name="run-1")
        trackio.log_model("alice/bert-ja", revision="v2")
        trackio.finish()

        assert not any("/api/models/" in url for url in server["gets"])
        assert server["patches"][0][1] == {
            "models": [{"repo_id": "alice/bert-ja", "revision": "v2"}]
        }

    def test_unresolvable_head_still_records_the_link(self, server):
        """A model that is not on the server (typo, never pushed) is kept: the
        UI shows it with a warning instead of losing the declaration."""
        server["get_error"] = OSError("connection refused")
        trackio.init("proj", name="run-1")
        with pytest.warns(UserWarning, match="could not resolve the current revision"):
            trackio.log_model("alice/ghost")
        trackio.finish()

        assert server["patches"][0][1] == {"models": [{"repo_id": "alice/ghost", "revision": ""}]}

    def test_all_models_go_in_one_request(self, server):
        trackio.init("proj", name="run-1")
        trackio.log_model("alice/one", revision="r1")
        trackio.log_model("alice/two", revision="r2")
        trackio.finish()

        assert len(server["patches"]) == 1
        assert server["patches"][0][1]["models"] == [
            {"repo_id": "alice/one", "revision": "r1"},
            {"repo_id": "alice/two", "revision": "r2"},
        ]

    def test_no_call_means_no_patch(self, server):
        """finish() must not send an empty list: models is a wholesale
        replace, so that would wipe whatever someone set in the UI."""
        trackio.init("proj", name="run-1")
        trackio.finish()
        assert server["patches"] == []

    def test_malformed_repo_id_is_ignored(self, server):
        trackio.init("proj", name="run-1")
        with pytest.warns(UserWarning, match="ignored"):
            trackio.log_model("no-namespace")
        trackio.finish()
        assert server["patches"] == []

    def test_run_name_with_a_slash_is_escaped(self, server):
        trackio.init("proj", name="sweep/run-1")
        trackio.log_model("alice/bert-ja", revision="v1")
        trackio.finish()

        assert server["patches"][0][0].endswith("/runs/sweep%2Frun-1")

    def test_patch_failure_never_raises(self, server, monkeypatch):
        def boom(*args, **kwargs):
            raise OSError("connection refused")

        trackio.init("proj", name="run-1")
        trackio.log_model("alice/bert-ja", revision="v1")
        monkeypatch.setattr(trackio.requests, "patch", boom)
        with pytest.warns(UserWarning, match="failed to record"):
            trackio.finish()

    def test_before_init_is_a_warning(self, server):
        trackio._current_run = None
        with pytest.warns(UserWarning, match="before init"):
            trackio.log_model("alice/bert-ja")


class TestOrdering:
    def test_models_are_recorded_after_the_run_exists(self, server, tmp_path):
        """A run that logged no points is only created by the finish call, so
        the annotation PATCH has to come after it."""
        trackio.init("proj", name="run-1")
        trackio.log_model("alice/bert-ja", revision="v1")
        trackio.finish()

        assert server["posts"], "finish() must have been posted"
        assert server["posts"][-1][0].endswith("/finish")
        assert server["patches"], "the models PATCH must have been sent"

    def test_artifacts_are_committed_even_without_metrics(self, server, tmp_path):
        f = tmp_path / "notes.txt"
        f.write_text("hello")
        trackio.init("proj", name="run-1")
        trackio.log_artifact(f)
        trackio.finish()

        assert server["commits"][0]["sources"] == [str(Path(f))]
