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


# -- fakes shared by both code paths -----------------------------------------


class _FakeRun:
    """Stand-in for the ``thinkingface.trackio._Run`` handle ``init()`` returns.

    Both integrations log and finish through *their own* handle rather than
    the module-level ``trackio.log()`` / ``trackio.finish()`` (which act on
    whatever run was started last), so this is where the calls a test asserts
    on land.
    """

    def __init__(self, config: dict[str, Any] | None = None) -> None:
        self.config = dict(config or {})
        self.logged: list[tuple[dict[str, Any], Any]] = []
        self.finished: list[str] = []

    def log(self, metrics: dict[str, Any], step: Any = None) -> None:
        self.logged.append((dict(metrics), step))

    def finish(self, status: str = "finished") -> None:
        self.finished.append(status)


@pytest.fixture
def no_global_logging(monkeypatch):
    """Make the module-level trackio.log()/finish() a test failure.

    They act on the module global ``_current_run``, which is exactly the run
    an integration holding its own handle must not use.
    """

    def _forbidden(*args: Any, **kwargs: Any) -> None:
        raise AssertionError(
            "the integration used the module-level trackio API instead of its own run handle"
        )

    monkeypatch.setattr(integrations.trackio, "log", _forbidden)
    monkeypatch.setattr(integrations.trackio, "finish", _forbidden)
    return _forbidden


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
    def test_full_flow_creates_run_logs_metrics_and_finishes(
        self, fake_transformers, no_global_logging, monkeypatch
    ):
        mod = fake_transformers
        assert mod._HAS_TRANSFORMERS is True

        init_calls: list[dict] = []
        run = _FakeRun()

        def fake_init(**kwargs):
            init_calls.append(kwargs)
            return run

        monkeypatch.setattr(mod.trackio, "init", fake_init)

        callback = mod.ThinkingFaceCallback(project="mnist", name="run1", config={"seed": 0})
        args = _FakeTrainingArguments(learning_rate=1e-3, num_train_epochs=3)
        state = _FakeState(global_step=0)
        control = object()

        callback.on_train_begin(args, state, control)
        assert len(init_calls) == 1
        init_kwargs = init_calls[0]
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
        # numeric metrics (including epoch) are forwarded; None is dropped, and
        # state.global_step is the step, not anything from logs
        assert run.logged == [({"loss": 0.5, "epoch": 1.0}, 5)]

        callback.on_train_end(args, state, control)
        assert run.finished == ["finished"]

    def test_two_callbacks_each_log_to_their_own_run(
        self, fake_transformers, no_global_logging, monkeypatch
    ):
        """Regression: the callback used to log through the module global.

        Two callbacks (or a trackio.init() for a side experiment between two
        on_log calls) rebind that global, so the second one's run received
        the first one's metrics and was the one finish() closed.
        """
        mod = fake_transformers
        runs: list[_FakeRun] = []

        def fake_init(**kwargs):
            runs.append(_FakeRun())
            return runs[-1]

        monkeypatch.setattr(mod.trackio, "init", fake_init)

        args_a = _FakeTrainingArguments()
        args_b = _FakeTrainingArguments(learning_rate=1e-3)
        control = object()

        first = mod.ThinkingFaceCallback(project="a")
        second = mod.ThinkingFaceCallback(project="b")
        first.on_train_begin(args_a, _FakeState(0), control)
        second.on_train_begin(args_b, _FakeState(0), control)

        first.on_log(args_a, _FakeState(1), control, logs={"loss": 1.0})
        second.on_log(args_b, _FakeState(2), control, logs={"loss": 2.0})
        first.on_train_end(args_a, _FakeState(1), control)

        assert runs[0].logged == [({"loss": 1.0}, 1)]
        assert runs[1].logged == [({"loss": 2.0}, 2)]
        assert runs[0].finished == ["finished"]
        assert runs[1].finished == []

    def test_on_log_drops_non_numeric_and_boolean_values(
        self, fake_transformers, no_global_logging, monkeypatch
    ):
        mod = fake_transformers
        run = _FakeRun()
        monkeypatch.setattr(mod.trackio, "init", lambda **kw: run)

        callback = mod.ThinkingFaceCallback(project="p")
        callback.on_train_begin(_FakeTrainingArguments(), _FakeState(0), object())
        callback.on_log(
            _FakeTrainingArguments(),
            _FakeState(0),
            object(),
            logs={"message": "ok", "is_world_process_zero": True, "loss": None},
        )
        # nothing numeric survives -> log() is never called
        assert run.logged == []

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
        self, fake_lightning, no_global_logging, monkeypatch
    ):
        mod = fake_lightning
        init_calls: list[dict] = []
        runs: list[_FakeRun] = []

        def fake_init(**kwargs):
            init_calls.append(kwargs)
            runs.append(_FakeRun(kwargs["config"]))
            return runs[-1]

        monkeypatch.setattr(mod.trackio, "init", fake_init)

        logger = mod.ThinkingFaceLightningLogger(project="mnist", config={"seed": 1})

        # hyperparams logged before any metric: no run created yet
        logger.log_hyperparams({"lr": 0.01})
        assert init_calls == []

        logger.log_metrics({"loss": 0.9, "note": "skip-me"}, step=3)
        assert len(init_calls) == 1
        assert init_calls[0]["config"] == {"seed": 1, "lr": 0.01}
        assert runs[0].logged == [({"loss": 0.9}, 3)]

    def test_hyperparams_after_run_started_update_run_config(
        self, fake_lightning, no_global_logging, monkeypatch
    ):
        mod = fake_lightning
        monkeypatch.setattr(mod.trackio, "init", lambda **kw: _FakeRun(kw["config"]))

        logger = mod.ThinkingFaceLightningLogger(project="mnist")
        logger.log_metrics({"loss": 1.0})
        logger.log_hyperparams({"batch_size": 32})
        assert logger._run.config["batch_size"] == 32

    def test_metrics_follow_the_logger_run_not_the_latest_init(
        self, fake_lightning, no_global_logging, monkeypatch
    ):
        """Regression: the logger used to log through the module global.

        A ``trackio.init()`` for a side experiment between two ``log_metrics``
        calls rebinds that global, so the logger's later metrics landed on the
        side run and ``finalize()`` marked the wrong run finished.
        """
        mod = fake_lightning
        runs: list[_FakeRun] = []

        def fake_init(**kwargs):
            runs.append(_FakeRun(kwargs["config"]))
            return runs[-1]

        monkeypatch.setattr(mod.trackio, "init", fake_init)

        logger = mod.ThinkingFaceLightningLogger(project="mnist")
        logger.log_metrics({"loss": 1.0}, step=1)
        side_run = fake_init(project="side", config={})  # a second run starts
        logger.log_metrics({"loss": 0.5}, step=2)
        logger.finalize("success")

        assert runs[0].logged == [({"loss": 1.0}, 1), ({"loss": 0.5}, 2)]
        assert runs[0].finished == ["finished"]
        assert side_run.logged == []
        assert side_run.finished == []

    def test_finalize_maps_status_and_clears_run(
        self, fake_lightning, no_global_logging, monkeypatch
    ):
        mod = fake_lightning
        runs: list[_FakeRun] = []

        def fake_init(**kwargs):
            runs.append(_FakeRun(kwargs["config"]))
            return runs[-1]

        monkeypatch.setattr(mod.trackio, "init", fake_init)

        logger = mod.ThinkingFaceLightningLogger(project="mnist")
        logger.log_metrics({"loss": 1.0})
        logger.finalize("success")
        assert runs[0].finished == ["finished"]
        assert logger._run is None

        logger.log_metrics({"loss": 1.0})
        logger.finalize("failed")
        assert runs[1].finished == ["failed"]

    def test_finalize_without_a_run_is_a_no_op(
        self, fake_lightning, no_global_logging, monkeypatch
    ):
        mod = fake_lightning
        init_calls = []
        monkeypatch.setattr(mod.trackio, "init", lambda **kw: init_calls.append(kw))
        logger = mod.ThinkingFaceLightningLogger(project="mnist")
        logger.finalize("success")
        assert init_calls == []

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
