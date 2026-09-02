"""Optional autolog integrations for :mod:`thinkingface.trackio`.

Provides a ``transformers.TrainerCallback`` and a PyTorch Lightning
``Logger`` that stream to a thinkingface server via :mod:`thinkingface.trackio`
(see that module's docstring for the underlying ``init``/``log``/``finish``
semantics), so a training loop built on ``transformers.Trainer`` or a
Lightning ``Trainer`` gets automatic run tracking without hand-written
``trackio.log(...)`` calls scattered through the loop.

Both ``transformers`` and ``lightning``/``pytorch_lightning`` are **optional**
dependencies of this package: importing this module never requires either of
them to be installed. Each integration class is therefore always importable;
only *instantiating* one raises a clear ``ImportError`` if its backing
library isn't available, so::

    from thinkingface.trackio.integrations import ThinkingFaceCallback

never fails on its own, regardless of what's installed.

.. code-block:: python

    from thinkingface.trackio.integrations import ThinkingFaceCallback

    trainer = Trainer(..., callbacks=[ThinkingFaceCallback(project="mnist")])
"""

from __future__ import annotations

import time
import warnings
from typing import Any

from thinkingface import trackio

__all__ = ["ThinkingFaceCallback", "ThinkingFaceLightningLogger"]


def _to_plain_dict(obj: Any) -> dict[str, Any]:
    """Best-effort conversion of a config-like object into a plain dict.

    Handles a plain ``dict``, an object exposing ``to_dict()``/``as_dict()``
    (``TrainingArguments``, several Lightning hparams containers), and a
    generic ``Namespace``-like object via ``vars()``. Never raises: on any
    failure this returns ``{}`` rather than breaking the caller, matching
    the best-effort philosophy of ``thinkingface._env_meta``.
    """
    if isinstance(obj, dict):
        return dict(obj)
    for attr in ("to_dict", "as_dict"):
        to_dict = getattr(obj, attr, None)
        if callable(to_dict):
            try:
                return dict(to_dict())
            except Exception:
                break
    try:
        return dict(vars(obj))
    except Exception:
        return {}


def _numeric_only(metrics: dict[str, Any] | None) -> dict[str, Any]:
    """Keep only real numeric values from a metrics-like dict.

    Both integrations below receive dicts that mix actual metrics with
    non-metric bookkeeping fields (``epoch``, occasional ``None``/string
    values, tensors that haven't been reduced to a Python scalar, ...).
    Only ``int``/``float`` values (``bool`` excluded, since it is a subclass
    of ``int`` but never an intended metric here) are forwarded to
    :func:`thinkingface.trackio.log`; everything else is silently dropped
    rather than sent to a server that expects numeric points.
    """
    if not metrics:
        return {}
    return {
        key: value
        for key, value in metrics.items()
        if isinstance(value, int | float) and not isinstance(value, bool)
    }


# -- transformers ------------------------------------------------------------

try:
    from transformers import TrainerCallback as _TrainerCallback

    _HAS_TRANSFORMERS = True
except ImportError:  # pragma: no cover - exercised via sys.modules faking in tests
    _TrainerCallback = object
    _HAS_TRANSFORMERS = False


class ThinkingFaceCallback(_TrainerCallback):
    """``transformers.TrainerCallback`` that streams to thinkingface.trackio.

    Equivalent to a ``report_to="wandb"``-style integration, but posting
    directly to a thinkingface server: a run is created in
    ``on_train_begin``, every ``on_log`` call is forwarded as metrics (using
    ``state.global_step`` as the step, since ``logs`` itself carries no step
    field), and ``on_train_end`` closes the run out.

    Requires ``transformers`` to be installed
    (``pip install "thinkingface[transformers]"``); constructing this class
    without it installed raises ``ImportError``. The class itself is always
    importable, so importing this module never depends on ``transformers``
    being present.

    Args:
        project: thinkingface experiment project name, forwarded to
            ``trackio.init()``.
        name: Run name, forwarded to ``trackio.init()``. Defaults to
            trackio's own ``run-{timestamp}`` naming when omitted.
        config: Extra config to record alongside the run. The
            ``TrainingArguments`` this callback observes are recorded
            separately under the ``_args`` key, so they never collide with
            the ``_meta`` namespace ``trackio.init()`` reserves for
            automatic environment metadata (see
            ``thinkingface._env_meta``) -- avoid using ``_args`` or
            ``_meta`` for your own values here.
        **init_kwargs: Forwarded to ``trackio.init()`` (e.g. ``resume=``,
            ``tags=``).

    Example::

        from thinkingface.trackio.integrations import ThinkingFaceCallback

        trainer = Trainer(..., callbacks=[ThinkingFaceCallback(project="mnist")])
    """

    def __init__(
        self,
        project: str,
        name: str | None = None,
        config: dict[str, Any] | None = None,
        **init_kwargs: Any,
    ) -> None:
        if not _HAS_TRANSFORMERS:
            raise ImportError(
                "ThinkingFaceCallback requires the `transformers` package. "
                'Install it with `pip install "thinkingface[transformers]"` '
                "or `pip install transformers`."
            )
        super().__init__()
        self.project = project
        self.name = name
        self.config = dict(config or {})
        self._init_kwargs = init_kwargs
        self._started = False
        # The run this callback owns. Everything below logs through this
        # handle rather than the module-level trackio.log()/finish(), which
        # act on whatever run was started last: a script with two callbacks,
        # or one that calls trackio.init() for a side experiment mid-training,
        # would otherwise send this Trainer's metrics to someone else's run
        # and finish the wrong one.
        self._run: Any = None

    def on_train_begin(self, args, state, control, **kwargs: Any) -> None:
        config = dict(self.config)
        config["_args"] = _to_plain_dict(args)
        self._run = trackio.init(
            project=self.project, name=self.name, config=config, **self._init_kwargs
        )
        self._started = True

    def on_log(
        self, args, state, control, logs: dict[str, Any] | None = None, **kwargs: Any
    ) -> None:
        if not self._started:
            # Defensive: Trainer always fires on_train_begin first, but a
            # nonstandard callback ordering or manual call shouldn't crash
            # the training loop.
            warnings.warn(
                "thinkingface.trackio.integrations: on_log() called before "
                "on_train_begin(); ignoring."
            )
            return
        metrics = _numeric_only(logs)
        if metrics and self._run is not None:
            self._run.log(metrics, step=state.global_step)

    def on_train_end(self, args, state, control, **kwargs: Any) -> None:
        if self._started:
            if self._run is not None:
                self._run.finish()
            self._run = None
            self._started = False


# -- PyTorch Lightning ---------------------------------------------------------

try:
    from lightning.pytorch.loggers.logger import Logger as _LightningLogger
    from lightning.pytorch.utilities import rank_zero_only

    _HAS_LIGHTNING = True
except ImportError:
    try:
        from pytorch_lightning.loggers.logger import Logger as _LightningLogger
        from pytorch_lightning.utilities import rank_zero_only

        _HAS_LIGHTNING = True
    except ImportError:  # pragma: no cover - exercised via sys.modules faking in tests
        _LightningLogger = object
        _HAS_LIGHTNING = False

        def rank_zero_only(fn):  # type: ignore[no-redef]
            return fn


class ThinkingFaceLightningLogger(_LightningLogger):
    """PyTorch Lightning ``Logger`` that streams to thinkingface.trackio.

    Implements the ``Logger`` interface (``name``, ``version``,
    ``log_hyperparams``, ``log_metrics``, ``finalize``) on top of
    :mod:`thinkingface.trackio`. The underlying run is created lazily, on
    the first call to ``log_metrics`` or ``log_hyperparams`` (whichever
    Lightning calls first), so hyperparameters logged before any metrics
    are still included in the run's initial config.

    Requires ``lightning`` (or the older standalone ``pytorch_lightning``
    package) to be installed
    (``pip install "thinkingface[lightning]"``); constructing this class
    without either installed raises ``ImportError``. The class itself is
    always importable, so importing this module never depends on Lightning
    being present.

    Args:
        project: thinkingface experiment project name, forwarded to
            ``trackio.init()``.
        name: Run name / Lightning "version", forwarded to
            ``trackio.init()``. Defaults to a ``run-{timestamp}`` name.
        config: Extra config to record alongside the run, merged with
            whatever ``log_hyperparams`` receives.
        **init_kwargs: Forwarded to ``trackio.init()``.

    Example::

        from thinkingface.trackio.integrations import ThinkingFaceLightningLogger

        trainer = pl.Trainer(logger=ThinkingFaceLightningLogger(project="mnist"))
    """

    def __init__(
        self,
        project: str,
        name: str | None = None,
        config: dict[str, Any] | None = None,
        **init_kwargs: Any,
    ) -> None:
        if not _HAS_LIGHTNING:
            raise ImportError(
                "ThinkingFaceLightningLogger requires `lightning` (or the "
                "older `pytorch_lightning` package). Install it with "
                '`pip install "thinkingface[lightning]"` or '
                "`pip install lightning`."
            )
        super().__init__()
        self._project = project
        self._name = name or f"run-{int(time.time())}"
        self._config: dict[str, Any] = dict(config or {})
        self._init_kwargs = init_kwargs
        self._run: Any = None

    @property
    def name(self) -> str:
        return self._project

    @property
    def version(self) -> str:
        return self._name

    @property
    def experiment(self) -> Any:
        """The underlying ``thinkingface.trackio`` run, started on first access."""
        return self._ensure_run()

    def _ensure_run(self) -> Any:
        if self._run is None:
            self._run = trackio.init(
                project=self._project,
                name=self._name,
                config=self._config,
                **self._init_kwargs,
            )
        return self._run

    @rank_zero_only
    def log_hyperparams(self, params: Any, *args: Any, **kwargs: Any) -> None:
        params_dict = _to_plain_dict(params)
        if self._run is None:
            # No run yet: fold straight into the config that init() will use.
            self._config.update(params_dict)
        else:
            # A run is already live (log_metrics happened first): update its
            # config in place so the next flush picks it up.
            self._run.config.update(params_dict)

    @rank_zero_only
    def log_metrics(self, metrics: dict[str, Any], step: int | None = None) -> None:
        run = self._ensure_run()
        numeric = _numeric_only(metrics)
        if numeric and run is not None:
            # Through the handle, not trackio.log(): the module-level helpers
            # act on the most recently started run, so a second logger -- or a
            # trackio.init() for a side experiment between two log_metrics
            # calls -- would silently redirect this logger's metrics.
            run.log(numeric, step=step)

    @rank_zero_only
    def finalize(self, status: str) -> None:
        if self._run is not None:
            self._run.finish(status="finished" if status == "success" else "failed")
            self._run = None
