"""Media and artifacts during a run (docs/dev/agent-features.md §2.8).

* ``trackio.Image`` / ``trackio.Table`` values in ``log()`` become the
  artifacts ``media/{key}/step_{step:08d}.png`` /
  ``tables/{key}/step_{step:08d}.parquet``, and the point carries no value
  for that key;
* a missing optional library (Pillow for arrays, pyarrow/pandas for tables)
  is one warning per run, never an exception;
* staged artifacts are committed in the background every
  ``THINKINGFACE_ARTIFACT_INTERVAL`` seconds, ``trackio.save()`` commits
  now, commits never overlap, and ``finish()`` commits the rest.

Pillow and numpy are not installed here: Pillow is faked through
``sys.modules`` and arrays by duck-typed stand-ins. pyarrow is a real
dependency, so parquet output is checked for real.
"""

from __future__ import annotations

import sys
import threading
import time
import types
import warnings
from pathlib import Path
from typing import Any

import pyarrow as pa
import pyarrow.parquet as pq
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
def _isolated_env(monkeypatch, tmp_path):
    monkeypatch.setenv("THINKINGFACE_ENDPOINT", "http://localhost:8080")
    monkeypatch.setenv("THINKINGFACE_REPO", "alice/exp")
    monkeypatch.setenv("THINKINGFACE_META", "off")
    monkeypatch.setenv("THINKINGFACE_SYSTEM_METRICS", "off")
    monkeypatch.setenv("THINKINGFACE_OFFLINE_DIR", str(tmp_path / "off"))
    monkeypatch.delenv("THINKINGFACE_TOKEN", raising=False)
    monkeypatch.setattr(trackio, "_FLUSH_INTERVAL_SECONDS", 3600.0)
    # No PIL unless a test installs the fake one.
    monkeypatch.setitem(sys.modules, "PIL", None)
    yield
    run = trackio._current_run
    if run is not None:
        if run._timer is not None:
            run._timer.cancel()
        run._upload_stop.set()
        run._finished = True
        trackio._current_run = None


@pytest.fixture
def server(monkeypatch):
    state: dict[str, Any] = {
        "posts": [],
        "commits": [],
        "commit_error": None,
        "commit_delay": 0.0,
        "active": 0,
        "max_active": 0,
    }
    guard = threading.Lock()

    def fake_get(url, headers=None, timeout=None):
        return _FakeResponse({"runs": []})

    def fake_post(url, json=None, headers=None, timeout=None):
        state["posts"].append((url, json))
        return _FakeResponse({"ok": True})

    monkeypatch.setattr(trackio.requests, "get", fake_get)
    monkeypatch.setattr(trackio.requests, "post", fake_post)
    monkeypatch.setattr(trackio.requests, "patch", fake_post)

    class _Add:
        def __init__(self, path_in_repo, path_or_fileobj):
            self.path_in_repo = path_in_repo
            self.path_or_fileobj = path_or_fileobj

    class _Api:
        def __init__(self, endpoint=None, token=None):
            pass

        def create_commit(self, repo_id, repo_type, operations, commit_message):
            with guard:
                state["active"] += 1
                state["max_active"] = max(state["max_active"], state["active"])
            try:
                if state["commit_delay"]:
                    time.sleep(state["commit_delay"])
                if state["commit_error"] is not None:
                    raise state["commit_error"]
                state["commits"].append(
                    {op.path_in_repo: Path(op.path_or_fileobj).read_bytes() for op in operations}
                )
            finally:
                with guard:
                    state["active"] -= 1

    stub = types.ModuleType("huggingface_hub")
    stub.CommitOperationAdd = _Add
    stub.HfApi = _Api
    monkeypatch.setitem(sys.modules, "huggingface_hub", stub)
    return state


@pytest.fixture
def fake_pil(monkeypatch):
    """A Pillow stand-in: images 'save' as a recognisable byte string."""
    saved: list[dict[str, Any]] = []

    class FakeImage:
        def __init__(self, source: Any) -> None:
            self.source = source

        def save(self, fp, format=None, pnginfo=None):
            saved.append({"format": format, "pnginfo": pnginfo, "source": self.source})
            Path(fp).write_bytes(b"\x89PNG-fake")

        def __enter__(self):
            return self

        def __exit__(self, *exc):
            return False

    FakeImage.__module__ = "PIL.Image"

    image_mod = types.ModuleType("PIL.Image")
    image_mod.open = lambda path: FakeImage(("file", str(path)))
    image_mod.fromarray = lambda array: FakeImage(("array", array))
    image_mod.FakeImage = FakeImage

    class PngInfo:
        def __init__(self):
            self.text: dict[str, str] = {}

        def add_text(self, key, value):
            self.text[key] = value

    png_mod = types.ModuleType("PIL.PngImagePlugin")
    png_mod.PngInfo = PngInfo

    pil = types.ModuleType("PIL")
    pil.Image = image_mod
    pil.PngImagePlugin = png_mod
    monkeypatch.setitem(sys.modules, "PIL", pil)
    monkeypatch.setitem(sys.modules, "PIL.Image", image_mod)
    monkeypatch.setitem(sys.modules, "PIL.PngImagePlugin", png_mod)
    return types.SimpleNamespace(saved=saved, Image=FakeImage)


def _log_points(state) -> list[dict[str, Any]]:
    return [p for url, body in state["posts"] if url.endswith("/log") for p in body["points"]]


def _committed(state) -> dict[str, bytes]:
    out: dict[str, bytes] = {}
    for commit in state["commits"]:
        out.update(commit)
    return out


def _png(tmp_path: Path, name: str = "grid.png") -> Path:
    f = tmp_path / name
    f.write_bytes(b"\x89PNG-real")
    return f


class _FakeArray:
    shape = (2, 2)
    dtype = "uint8"


class TestImage:
    def test_a_png_file_is_committed_under_its_step(self, server, tmp_path):
        trackio.init("proj", name="r1")
        trackio.log({"samples": trackio.Image(_png(tmp_path)), "loss": 0.5}, step=10)
        trackio.finish()

        assert _committed(server) == {
            "proj/artifacts/r1/media/samples/step_00000010.png": b"\x89PNG-real"
        }
        (point,) = _log_points(server)
        assert point["metrics"] == {"loss": 0.5}  # no value for the media key
        assert point["step"] == 10

    def test_a_media_only_log_makes_no_point_but_advances_the_step(self, server, tmp_path):
        run = trackio.init("proj", name="r2")
        trackio.log({"samples": trackio.Image(_png(tmp_path))})
        trackio.log({"loss": 1.0})
        run.flush()
        assert [p["step"] for p in _log_points(server)] == [1]

    def test_nested_keys_keep_their_layout(self, server, tmp_path):
        trackio.init("proj", name="r3")
        trackio.log({"eval/samples": trackio.Image(_png(tmp_path))}, step=3)
        trackio.finish()
        assert list(_committed(server)) == [
            "proj/artifacts/r3/media/eval/samples/step_00000003.png"
        ]

    def test_a_key_that_climbs_out_is_refused(self, server, tmp_path):
        trackio.init("proj", name="r4")
        with pytest.warns(UserWarning, match="not logging"):
            trackio.log({"../../x": trackio.Image(_png(tmp_path))})
        trackio.finish()
        assert server["commits"] == []

    def test_a_non_png_file_without_pillow_keeps_its_format(self, server, tmp_path):
        jpg = tmp_path / "photo.JPG"
        jpg.write_bytes(b"jpeg")
        trackio.init("proj", name="r5")
        trackio.log({"photo": trackio.Image(jpg)}, step=1)
        trackio.finish()
        assert _committed(server) == {"proj/artifacts/r5/media/photo/step_00000001.jpg": b"jpeg"}

    def test_an_array_without_pillow_warns_once_and_is_skipped(self, server, tmp_path):
        trackio.init("proj", name="r6")
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            for step in range(3):
                trackio.log({"img": trackio.Image(_FakeArray()), "loss": 1.0}, step=step)
        messages = [str(w.message) for w in caught if "Pillow" in str(w.message)]
        assert len(messages) == 1
        trackio.finish()
        assert server["commits"] == []
        assert len(_log_points(server)) == 3  # the metrics were not held hostage

    def test_a_missing_file_warns(self, server, tmp_path):
        trackio.init("proj", name="r7")
        with pytest.warns(UserWarning, match="does not exist"):
            trackio.log({"img": trackio.Image(tmp_path / "nope.png")})

    def test_a_pil_image_is_encoded_with_its_caption(self, server, tmp_path, fake_pil):
        trackio.init("proj", name="r8")
        trackio.log({"img": trackio.Image(fake_pil.Image("x"), caption="a cat")}, step=2)
        trackio.finish()
        assert _committed(server) == {
            "proj/artifacts/r8/media/img/step_00000002.png": b"\x89PNG-fake"
        }
        (saved,) = fake_pil.saved
        assert saved["format"] == "PNG"
        assert saved["pnginfo"].text == {"Caption": "a cat"}

    def test_a_non_png_file_is_converted_when_pillow_is_there(self, server, tmp_path, fake_pil):
        jpg = tmp_path / "photo.jpg"
        jpg.write_bytes(b"jpeg")
        trackio.init("proj", name="r9")
        trackio.log({"photo": trackio.Image(jpg)}, step=1)
        trackio.finish()
        assert list(_committed(server)) == ["proj/artifacts/r9/media/photo/step_00000001.png"]

    def test_a_real_array_is_encoded(self, server, tmp_path, fake_pil):
        np = pytest.importorskip("numpy")
        trackio.init("proj", name="r10")
        trackio.log({"img": trackio.Image(np.zeros((4, 4, 3), dtype=np.float32))}, step=0)
        trackio.finish()
        assert list(_committed(server)) == ["proj/artifacts/r10/media/img/step_00000000.png"]


class TestTable:
    def _read(self, data: bytes, tmp_path: Path) -> pa.Table:
        f = tmp_path / "read.parquet"
        f.write_bytes(data)
        return pq.read_table(f)

    def test_rows_and_columns(self, server, tmp_path):
        trackio.init("proj", name="t1")
        table = trackio.Table(columns=["text", "score"], data=[["a", 0.5], ["b", 0.25]])
        trackio.log({"preds": table, "loss": 1.0}, step=7)
        trackio.finish()

        committed = _committed(server)
        assert list(committed) == ["proj/artifacts/t1/tables/preds/step_00000007.parquet"]
        read = self._read(
            committed["proj/artifacts/t1/tables/preds/step_00000007.parquet"], tmp_path
        )
        assert read.to_pylist() == [{"text": "a", "score": 0.5}, {"text": "b", "score": 0.25}]
        assert _log_points(server)[0]["metrics"] == {"loss": 1.0}

    def test_rows_as_dicts_and_an_arrow_table(self, server, tmp_path):
        trackio.init("proj", name="t2")
        trackio.log({"a": trackio.Table(data=[{"x": 1}, {"x": 2}])}, step=0)
        trackio.log({"b": trackio.Table(dataframe=pa.table({"y": [3]}))}, step=0)
        trackio.finish()
        committed = _committed(server)
        a = self._read(committed["proj/artifacts/t2/tables/a/step_00000000.parquet"], tmp_path)
        b = self._read(committed["proj/artifacts/t2/tables/b/step_00000000.parquet"], tmp_path)
        assert a.to_pylist() == [{"x": 1}, {"x": 2}]
        assert b.to_pylist() == [{"y": 3}]

    def test_a_ragged_row_warns(self, server):
        trackio.init("proj", name="t3")
        with pytest.warns(UserWarning, match="2 columns"):
            trackio.log({"t": trackio.Table(columns=["a", "b"], data=[[1]])})

    def test_needs_data(self):
        with pytest.raises(ValueError):
            trackio.Table()

    def test_without_pyarrow_or_pandas_it_warns_once(self, server, monkeypatch):
        for name in ("pyarrow", "pyarrow.parquet", "pandas"):
            monkeypatch.setitem(sys.modules, name, None)
        trackio.init("proj", name="t4")
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            trackio.log({"t": trackio.Table(data=[[1]])})
            trackio.log({"t": trackio.Table(data=[[2]])})
        assert len([w for w in caught if "pyarrow or pandas" in str(w.message)]) == 1


class TestBackgroundCommits:
    def test_staged_artifacts_are_committed_without_finish(self, server, tmp_path, monkeypatch):
        monkeypatch.setenv("THINKINGFACE_ARTIFACT_INTERVAL", "0.05")
        trackio.init("proj", name="b1")
        trackio.log({"img": trackio.Image(_png(tmp_path))}, step=1)
        trackio.log_artifact(_png(tmp_path, "other.png"))
        for _ in range(200):
            if server["commits"]:
                break
            time.sleep(0.01)
        assert len(server["commits"]) == 1
        assert sorted(server["commits"][0]) == [
            "proj/artifacts/b1/media/img/step_00000001.png",
            "proj/artifacts/b1/other.png",
        ]

    def test_save_commits_now(self, server, tmp_path):
        trackio.init("proj", name="b2")
        trackio.log_artifact(_png(tmp_path))
        trackio.save()
        assert list(_committed(server)) == ["proj/artifacts/b2/grid.png"]
        trackio.finish()
        assert len(server["commits"]) == 1  # nothing left for finish()

    def test_a_failed_save_is_retried_by_finish(self, server, tmp_path):
        trackio.init("proj", name="b3")
        trackio.log_artifact(_png(tmp_path))
        server["commit_error"] = ConnectionError("down")
        with pytest.warns(UserWarning, match="will retry"):
            trackio.save()
        server["commit_error"] = None
        trackio.finish()
        assert list(_committed(server)) == ["proj/artifacts/b3/grid.png"]

    def test_commits_never_overlap(self, server, tmp_path, monkeypatch):
        monkeypatch.setenv("THINKINGFACE_ARTIFACT_INTERVAL", "0.01")
        server["commit_delay"] = 0.05
        run = trackio.init("proj", name="b4")
        savers = []
        for i in range(5):
            run.log_artifact(_png(tmp_path, f"{i}.png"))
            t = threading.Thread(target=run.save)
            t.start()
            savers.append(t)
        for t in savers:
            t.join(5)
        run.finish()
        assert server["max_active"] == 1
        assert sorted(_committed(server)) == [f"proj/artifacts/b4/{i}.png" for i in range(5)]

    def test_the_same_name_twice_is_one_file_in_the_commit(self, server, tmp_path):
        trackio.init("proj", name="b5")
        trackio.log({"img": trackio.Image(_png(tmp_path))}, step=1)
        trackio.log({"img": trackio.Image(_png(tmp_path))}, step=1)
        trackio.finish()
        (commit,) = server["commits"]
        assert list(commit) == ["proj/artifacts/b5/media/img/step_00000001.png"]

    def test_media_files_are_cleaned_up_and_user_files_are_not(self, server, tmp_path):
        user_file = _png(tmp_path, "keep.png")
        run = trackio.init("proj", name="b6")
        trackio.log({"img": trackio.Image(_png(tmp_path))}, step=1)
        trackio.log_artifact(user_file)
        media_dir = run._media_dir
        assert media_dir is not None and media_dir.exists()
        trackio.save()
        assert not any(p.is_file() for p in media_dir.rglob("*"))
        trackio.finish()
        assert not media_dir.exists()
        assert user_file.exists()

    def test_zero_interval_holds_everything_for_finish(self, server, tmp_path, monkeypatch):
        monkeypatch.setenv("THINKINGFACE_ARTIFACT_INTERVAL", "0")
        run = trackio.init("proj", name="b7")
        trackio.log_artifact(_png(tmp_path))
        assert run._upload_thread is None
        trackio.finish()
        assert len(server["commits"]) == 1


class TestOfflineMedia:
    def test_media_goes_into_the_run_directory(self, tmp_path, monkeypatch, capsys):
        def refuse(*a, **k):
            raise AssertionError("no network offline")

        monkeypatch.setattr(trackio.requests, "post", refuse)
        monkeypatch.setattr(trackio.requests, "get", refuse)
        trackio.init("proj", name="o1", mode="offline")
        trackio.log({"img": trackio.Image(_png(tmp_path)), "loss": 1.0}, step=5)
        trackio.log({"t": trackio.Table(data=[[1]])}, step=5)
        trackio.save()
        trackio.finish()

        (run_dir,) = list((tmp_path / "off").iterdir())
        import json

        records = [json.loads(line) for line in (run_dir / "run.jsonl").read_text().splitlines()]
        artifacts = [r for r in records if r["type"] == "artifact"]
        assert artifacts == [
            {
                "v": 1,
                "type": "artifact",
                "name": "media/img/step_00000005.png",
                "path": "artifacts/media/img/step_00000005.png",
            },
            {
                "v": 1,
                "type": "artifact",
                "name": "tables/t/step_00000005.parquet",
                "path": "artifacts/tables/t/step_00000005.parquet",
            },
        ]
        assert (run_dir / "artifacts/media/img/step_00000005.png").read_bytes() == b"\x89PNG-real"
        assert pq.read_table(run_dir / "artifacts/tables/t/step_00000005.parquet").num_rows == 1
