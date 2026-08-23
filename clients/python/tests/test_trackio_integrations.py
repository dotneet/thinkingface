"""Tests for thinkingface.trackio.integrations.

The primary contract under test (see todo/exp-autolog-trainer.md) is that
this module is importable -- and its callback/logger classes are
importable -- regardless of whether `transformers` or `lightning` /
`pytorch_lightning` are installed. Neither is installed in this test
environment, so most of that contract is exercised "for free" simply by
importing the module normally.

To also verify the actual autolog *behavior* (init/log/finish wiring,
TrainingArguments capture, numeric-only metric filtering) without requiring
a real `transformers`/`lightning` install, the relevant tests inject
minimal fake modules into `sys.modules` and reload the integrations module
so it picks up the fakes, mirroring how the real libraries would be picked
up if installed.
"""

from __future__ import annotations

import importlib
import sys
import types
from typing import Any

import pytest

import thinkingface.trackio.integrations as integrations


def _reload_integrations():
    return importlib.reload(sys.modules["thinkingface.trackio.integrations"])


@pytest.fixture(autouse=True)
def _restore_integrations_module():
    """Ensure every test starts and ends with the "nothing installed" state.

    Individual tests inject fake `transformers`/`lightning` modules and
    reload `thinkingface.trackio.integrations` to pick them up; without this
    the reloaded module object (mutated in place by `importlib.reload`)
    would leak `_HAS_TRANSFORMERS = True` / `_HAS_LIGHTNING = True` into
    unrelated tests that run afterwards.
    """
    yield
    for name in list(sys.modules):
        if name == "transformers" or name.startswith(("lightning", "pytorch_lightning")):
            del sys.modules[name]
    _reload_integrations()


class TestImportNeverBreaks:
    def test_thinkingface_trackio_import_ok(self):
        import thinkingface  # noqa: F401
        import thinkingface.trackio  # noqa: F401

    def test_integrations_import_ok_without_optional_deps(self):
        mod = _reload_integrations()
        assert mod._HAS_TRANSFORMERS is False
        assert mod._HAS_LIGHTNING is False

    def test_callback_classes_always_importable(self):
        from thinkingface.trackio.integrations import (
            ThinkingFaceCallback,
            ThinkingFaceLightningLogger,
        )

        assert ThinkingFaceCallback is not None
        assert ThinkingFaceLightningLogger is not None

    def test_instantiating_callback_without_transformers_raises_import_error(self):
        with pytest.raises(ImportError, match="transformers"):
            integrations.ThinkingFaceCallback(project="p")

    def test_instantiating_lightning_logger_without_lightning_raises_import_error(self):
        with pytest.raises(ImportError, match="lightning"):
            integrations.ThinkingFaceLightningLogger(project="p")


# -- fakes used to exercise the transformers-backed code path ----------------


class _FakeTrainerCallback:
    """Stand-in for transformers.TrainerCallback (a plain no-op base class)."""


class _FakeTrainingArguments:
    def __init__(self, **kwargs: Any) -> None:
        self._data = kwargs

    def to_dict(self) -> dict[str, Any]:
        return dict(self._data)


class _FakeState:
    def __init__(self, global_step: int) -> None:
        self.global_step = global_step


@pytest.fixture
def fake_transformers(monkeypatch):
    fake_module = types.ModuleType("transformers")
    fake_module.TrainerCallback = _FakeTrainerCallback
    monkeypatch.setitem(sys.modules, "transformers", fake_module)
    return _reload_integrations()


class TestThinkingFaceCallback:
    def test_full_flow_creates_run_logs_metrics_and_finishes(self, fake_transformers, monkeypatch):
        mod = fake_transformers
        assert mod._HAS_TRANSFORMERS is True

        calls: list[tuple] = []
        monkeypatch.setattr(mod.trackio, "init", lambda **kw: calls.append(("init", kw)))
        monkeypatch.setattr(
            mod.trackio, "log", lambda metrics, step=None: calls.append(("log", metrics, step))
        )
        monkeypatch.setattr(mod.trackio, "finish", lambda **kw: calls.append(("finish", kw)))

        callback = mod.ThinkingFaceCallback(project="mnist", name="run1", config={"seed": 0})
        args = _FakeTrainingArguments(learning_rate=1e-3, num_train_epochs=3)
        state = _FakeState(global_step=0)
        control = object()

        callback.on_train_begin(args, state, control)
        assert calls[0][0] == "init"
        init_kwargs = calls[0][1]
        assert init_kwargs["project"] == "mnist"
        assert init_kwargs["name"] == "run1"
        # user config and TrainingArguments coexist under separate namespaces
        assert init_kwargs["config"]["seed"] == 0
        assert init_kwargs["config"]["_args"] == {
            "learning_rate": 1e-3,
            "num_train_epochs": 3,
        }

        state.global_step = 5
        callback.on_log(
            args, state, control, logs={"loss": 0.5, "epoch": 1.0, "eval_runtime": None}
        )
        assert calls[1][0] == "log"
        # numeric metrics (including epoch) are forwarded; None is dropped
        assert calls[1][1] == {"loss": 0.5, "epoch": 1.0}
        # state.global_step is used as the step, not anything from logs
        assert calls[1][2] == 5

        callback.on_train_end(args, state, control)
        assert calls[2] == ("finish", {})

    def test_on_log_drops_non_numeric_and_boolean_values(self, fake_transformers, monkeypatch):
        mod = fake_transformers
        log_calls: list[dict] = []
        monkeypatch.setattr(mod.trackio, "init", lambda **kw: None)
        monkeypatch.setattr(
            mod.trackio, "log", lambda metrics, step=None: log_calls.append(metrics)
        )

        callback = mod.ThinkingFaceCallback(project="p")
        callback.on_train_begin(_FakeTrainingArguments(), _FakeState(0), object())
        callback.on_log(
            _FakeTrainingArguments(),
            _FakeState(0),
            object(),
            logs={"message": "ok", "is_world_process_zero": True, "loss": None},
        )
        # nothing numeric survives -> log() is never called
        assert log_calls == []

    def test_on_log_before_train_begin_warns_instead_of_raising(self, fake_transformers):
        mod = fake_transformers
        callback = mod.ThinkingFaceCallback(project="p")
        with pytest.warns(UserWarning, match="on_train_begin"):
            callback.on_log(_FakeTrainingArguments(), _FakeState(0), object(), logs={"loss": 1.0})

    def test_on_train_end_without_start_is_a_no_op(self, fake_transformers, monkeypatch):
        mod = fake_transformers
        finish_calls = []
        monkeypatch.setattr(mod.trackio, "finish", lambda **kw: finish_calls.append(kw))
        callback = mod.ThinkingFaceCallback(project="p")
        callback.on_train_end(_FakeTrainingArguments(), _FakeState(0), object())
        assert finish_calls == []


# -- fakes used to exercise the Lightning-backed code path --------------------


class _FakeLightningLoggerBase:
    """Stand-in for lightning.pytorch.loggers.logger.Logger."""


class _FakeRun:
    def __init__(self, config: dict[str, Any]) -> None:
        self.config = config


@pytest.fixture
def fake_lightning(monkeypatch):
    lightning_pkg = types.ModuleType("lightning")
    pytorch_pkg = types.ModuleType("lightning.pytorch")
    loggers_pkg = types.ModuleType("lightning.pytorch.loggers")
    logger_mod = types.ModuleType("lightning.pytorch.loggers.logger")
    logger_mod.Logger = _FakeLightningLoggerBase
    utilities_mod = types.ModuleType("lightning.pytorch.utilities")
    utilities_mod.rank_zero_only = lambda fn: fn

    lightning_pkg.pytorch = pytorch_pkg
    pytorch_pkg.loggers = loggers_pkg
    pytorch_pkg.utilities = utilities_mod
    loggers_pkg.logger = logger_mod

    fake_modules = {
        "lightning": lightning_pkg,
        "lightning.pytorch": pytorch_pkg,
        "lightning.pytorch.loggers": loggers_pkg,
        "lightning.pytorch.loggers.logger": logger_mod,
        "lightning.pytorch.utilities": utilities_mod,
    }
    for name, mod in fake_modules.items():
        monkeypatch.setitem(sys.modules, name, mod)

    return _reload_integrations()


class TestThinkingFaceLightningLogger:
    def test_name_and_version(self, fake_lightning):
        mod = fake_lightning
        assert mod._HAS_LIGHTNING is True
        logger = mod.ThinkingFaceLightningLogger(project="mnist", name="run1")
        assert logger.name == "mnist"
        assert logger.version == "run1"

    def test_hyperparams_before_first_metric_fold_into_init_config(
        self, fake_lightning, monkeypatch
    ):
        mod = fake_lightning
        calls: list[tuple] = []

        def fake_init(**kwargs):
            calls.append(("init", kwargs))
            return _FakeRun(dict(kwargs["config"]))

        monkeypatch.setattr(mod.trackio, "init", fake_init)
        monkeypatch.setattr(
            mod.trackio, "log", lambda metrics, step=None: calls.append(("log", metrics, step))
        )

        logger = mod.ThinkingFaceLightningLogger(project="mnist", config={"seed": 1})

        # hyperparams logged before any metric: no run created yet
        logger.log_hyperparams({"lr": 0.01})
        assert calls == []

        logger.log_metrics({"loss": 0.9, "note": "skip-me"}, step=3)
        assert calls[0][0] == "init"
        assert calls[0][1]["config"] == {"seed": 1, "lr": 0.01}
        assert calls[1] == ("log", {"loss": 0.9}, 3)

    def test_hyperparams_after_run_started_update_run_config(self, fake_lightning, monkeypatch):
        mod = fake_lightning
        monkeypatch.setattr(mod.trackio, "init", lambda **kw: _FakeRun(dict(kw["config"])))
        monkeypatch.setattr(mod.trackio, "log", lambda metrics, step=None: None)

        logger = mod.ThinkingFaceLightningLogger(project="mnist")
        logger.log_metrics({"loss": 1.0})
        logger.log_hyperparams({"batch_size": 32})
        assert logger._run.config["batch_size"] == 32

    def test_finalize_maps_status_and_clears_run(self, fake_lightning, monkeypatch):
        mod = fake_lightning
        finish_calls: list[dict] = []
        monkeypatch.setattr(mod.trackio, "init", lambda **kw: _FakeRun(dict(kw["config"])))
        monkeypatch.setattr(mod.trackio, "log", lambda metrics, step=None: None)
        monkeypatch.setattr(mod.trackio, "finish", lambda **kw: finish_calls.append(kw))

        logger = mod.ThinkingFaceLightningLogger(project="mnist")
        logger.log_metrics({"loss": 1.0})
        logger.finalize("success")
        assert finish_calls == [{"status": "finished"}]
        assert logger._run is None

        logger.log_metrics({"loss": 1.0})
        logger.finalize("failed")
        assert finish_calls[-1] == {"status": "failed"}

    def test_finalize_without_a_run_is_a_no_op(self, fake_lightning, monkeypatch):
        mod = fake_lightning
        finish_calls = []
        monkeypatch.setattr(mod.trackio, "finish", lambda **kw: finish_calls.append(kw))
        logger = mod.ThinkingFaceLightningLogger(project="mnist")
        logger.finalize("success")
        assert finish_calls == []

    def test_experiment_property_lazily_starts_run(self, fake_lightning, monkeypatch):
        mod = fake_lightning
        init_calls = []
        monkeypatch.setattr(
            mod.trackio,
            "init",
            lambda **kw: init_calls.append(kw) or _FakeRun(dict(kw["config"])),
        )
        logger = mod.ThinkingFaceLightningLogger(project="mnist")
        assert init_calls == []
        run = logger.experiment
        assert isinstance(run, _FakeRun)
        assert len(init_calls) == 1
