"""Config values JSON cannot carry are converted, not allowed to sink the config.

Before this, one ``pathlib.Path`` in ``config`` made ``requests`` fail to
encode the batch, and the shim then sent the points *without any config at
all*. ``_sanitize.sanitize`` now converts every value it recognises and falls
back to ``str(value)`` for the rest, reporting those keys so the run warns
once.

numpy is not installed in this environment (and the shim must never import
it), so numpy values are faked by classes whose ``__module__`` is ``numpy``
-- which is exactly what the duck typing looks at.
"""

from __future__ import annotations

import argparse
import dataclasses
import datetime as dt
import enum
import json
import math
import threading
import warnings
from pathlib import Path, PurePosixPath
from typing import Any

import pytest

import thinkingface.trackio as trackio
from thinkingface.trackio import _sanitize


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
    state: dict[str, Any] = {"posts": [], "runs": []}

    def fake_get(url, headers=None, timeout=None):
        return _FakeResponse({"runs": state["runs"]})

    def fake_post(url, json=None, headers=None, timeout=None):
        # What requests does with json=: strict encoding before sending.
        import json as _json

        _json.dumps(json, allow_nan=False)
        state["posts"].append((url, json))
        return _FakeResponse({"ok": True})

    monkeypatch.setattr(trackio.requests, "get", fake_get)
    monkeypatch.setattr(trackio.requests, "post", fake_post)
    return state


def _log_configs(state) -> list[Any]:
    return [body.get("config") for url, body in state["posts"] if url.endswith("/log")]


# -- fakes ------------------------------------------------------------------


class _NumpyFloat:
    """Stands in for np.float32: not a Python float, has .item()."""

    def __init__(self, value: float) -> None:
        self.value = value
        self.shape = ()

    def item(self) -> float:
        return self.value


_NumpyFloat.__module__ = "numpy"


class _NumpyArray:
    def __init__(self, values: list[Any], shape: tuple[int, ...]) -> None:
        self.values = values
        self.shape = shape
        self.size = math.prod(shape)
        self.dtype = "float32"

    def tolist(self) -> list[Any]:
        return self.values

    def item(self) -> Any:
        raise ValueError("only size-1 arrays")


_NumpyArray.__module__ = "numpy"


class Optimizer(enum.Enum):
    ADAM = "adam"
    SGD = "sgd"


class Level(enum.IntEnum):
    LOW = 1


class Weird(enum.Enum):
    LOCKED = threading.Lock()


@dataclasses.dataclass
class ModelConfig:
    hidden: int
    out: Path
    optimizer: Optimizer = Optimizer.ADAM


# -- the converter ------------------------------------------------------------


class TestSanitize:
    def test_json_values_pass_through_unchanged(self):
        value = {"a": 1, "b": 0.5, "c": "x", "d": None, "e": True, "f": [1, {"g": 2}]}
        assert _sanitize.sanitize(value) == (value, [])

    @pytest.mark.parametrize(
        ("value", "expected"),
        [
            (Path("/tmp/out"), "/tmp/out"),
            (PurePosixPath("a/b"), "a/b"),
            (Optimizer.ADAM, "adam"),
            (Level.LOW, 1),
            (dt.datetime(2026, 9, 27, 12, 0, tzinfo=dt.timezone.utc), "2026-09-27T12:00:00+00:00"),
            (dt.date(2026, 9, 27), "2026-09-27"),
            ((1, 2), [1, 2]),
            (float("nan"), "nan"),
            (float("inf"), "inf"),
            (float("-inf"), "-inf"),
            (_NumpyFloat(0.25), 0.25),
            (_NumpyFloat(float("nan")), "nan"),
        ],
    )
    def test_explicit_conversions(self, value, expected):
        out, fallback = _sanitize.sanitize({"k": value})
        assert out == {"k": expected}
        assert fallback == []

    def test_an_enum_whose_value_cannot_be_encoded_uses_its_name(self):
        assert _sanitize.sanitize({"k": Weird.LOCKED}) == ({"k": "LOCKED"}, [])

    def test_sets_become_sorted_lists(self):
        assert _sanitize.sanitize({"k": {"b", "a", "c"}}) == ({"k": ["a", "b", "c"]}, [])

    def test_non_string_keys_become_strings(self):
        out, _ = _sanitize.sanitize({1: "a", (2, 3): "b", Optimizer.SGD: "c"})
        assert out == {"1": "a", "(2, 3)": "b", "SGD": "c"}

    def test_dataclass_and_namespace_become_their_fields(self):
        ns = argparse.Namespace(lr=0.1, model=ModelConfig(hidden=8, out=Path("o")))
        out, fallback = _sanitize.sanitize({"args": ns})
        assert out == {"args": {"lr": 0.1, "model": {"hidden": 8, "out": "o", "optimizer": "adam"}}}
        assert fallback == []

    def test_a_small_array_is_expanded_and_a_large_one_summarised(self):
        small = _NumpyArray([[1.0, float("nan")]], (1, 2))
        large = _NumpyArray([], (2000,))
        out, fallback = _sanitize.sanitize({"small": small, "large": large})
        assert out["small"] == [[1.0, "nan"]]
        assert out["large"] == "ndarray(shape=(2000,), dtype=float32)"
        assert fallback == []

    def test_unrecognised_values_fall_back_to_str_and_are_reported(self):
        lock = threading.Lock()
        out, fallback = _sanitize.sanitize({"a": {"b": lock}, "c": b"raw", "ok": 1})
        assert out == {"a": {"b": str(lock)}, "c": str(b"raw"), "ok": 1}
        assert fallback == ["a.b", "c"]

    def test_a_cycle_does_not_recurse_forever(self):
        loop: dict[str, Any] = {"x": 1}
        loop["self"] = loop
        out, fallback = _sanitize.sanitize(loop)
        assert out["x"] == 1
        assert isinstance(out["self"], str)
        assert fallback == ["self"]

    def test_the_result_is_strict_json(self):
        cfg = {
            "p": Path("x"),
            "e": Optimizer.SGD,
            "n": float("nan"),
            "s": {1, 2},
            "np": _NumpyFloat(1.5),
            "o": object(),
        }
        out, _ = _sanitize.sanitize(cfg)
        json.dumps(out, allow_nan=False)

    def test_to_config_dict(self):
        assert _sanitize.to_config_dict(None) == {}
        assert _sanitize.to_config_dict(argparse.Namespace(a=1)) == {"a": 1}
        assert _sanitize.to_config_dict(ModelConfig(1, Path("o")))["hidden"] == 1
        with pytest.raises(TypeError):
            _sanitize.to_config_dict(42)


# -- through a run ----------------------------------------------------------


class TestConfigThroughARun:
    def test_a_path_no_longer_drops_the_whole_config(self, server):
        run = trackio.init("proj", name="r1", config={"lr": 0.1, "out": Path("/data/out")})
        run.log({"loss": 1.0})
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            run.flush()
        assert [str(w.message) for w in caught] == []
        assert _log_configs(server) == [{"lr": 0.1, "out": "/data/out"}]

    def test_init_accepts_an_argparse_namespace(self, server):
        args = argparse.Namespace(lr=0.01, epochs=3, data=Path("d"))
        run = trackio.init("proj", name="r2", config=args)
        assert run.config == {"lr": 0.01, "epochs": 3, "data": Path("d")}
        run.log({"loss": 1.0})
        run.flush()
        assert _log_configs(server) == [{"lr": 0.01, "epochs": 3, "data": "d"}]

    def test_the_str_fallback_warns_once_naming_the_keys(self, server):
        run = trackio.init("proj", name="r3", config={"handle": object(), "lr": 0.1})
        run.log({"loss": 1.0})
        with pytest.warns(UserWarning, match=r"'handle' of run 'r3' are not JSON types"):
            run.flush()

        run.config["other"] = object()
        run.log({"loss": 2.0})
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            run.flush()
        assert [str(w.message) for w in caught if "not JSON types" in str(w.message)] == []

    def test_an_unchanged_config_is_not_resent(self, server):
        run = trackio.init("proj", name="r4", config={"out": Path("o"), "s": {1, 2}})
        for step in range(3):
            run.log({"loss": float(step)})
            run.flush()
        assert _log_configs(server) == [{"out": "o", "s": [1, 2]}, None, None]

    def test_resume_does_not_report_a_path_as_a_change(self, server):
        server["runs"] = [{"name": "r5", "last_step": 4, "config": {"out": "o", "lr": 0.1}}]
        run = trackio.init("proj", name="r5", resume="allow", config={"out": Path("o"), "lr": 0.2})
        assert run.config["_resume"]["config_changes"] == {"lr": {"from": 0.1, "to": 0.2}}
