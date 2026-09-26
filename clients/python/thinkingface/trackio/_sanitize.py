"""Turn a run's ``config`` into something JSON can carry.

A training script's config is whatever its author had to hand: an
``argparse.Namespace``, a dataclass, a ``pathlib.Path`` for the output
directory, an ``Enum`` for the optimiser, a numpy scalar that came out of a
schedule. ``requests`` serialises with strict ``json.dumps``, so a single one of
those used to make the whole config unsendable -- and the shim then sent the
points without any config at all.

``sanitize`` converts every value it recognises explicitly and falls back to
``str(value)`` (the ``default=str`` of ``json.dumps``) for the rest, reporting
which keys needed that fallback so the caller can say so once. numpy is never
imported: its values are recognised by duck typing.
"""

from __future__ import annotations

import argparse
import dataclasses
import datetime as _dt
import enum
import json
import math
import numbers
import pathlib
from collections.abc import Mapping
from typing import Any

# A numpy array with more elements than this is summarised rather than
# expanded: a config is meant to be a handful of hyperparameters, and a
# million-element list would dwarf every metric the run sends.
MAX_ARRAY_ELEMENTS = 1000

# Nesting deeper than this is almost certainly an object graph, not a config.
_MAX_DEPTH = 32


class _Tracker:
    """Collects the dotted paths of values that needed the ``str`` fallback."""

    def __init__(self) -> None:
        self.fallback: list[str] = []


def sanitize(value: Any) -> tuple[Any, list[str]]:
    """Return a JSON-encodable copy of ``value`` and the dotted paths of the
    values that were stringified by the generic fallback.

    The result is always made of ``dict`` (str keys) / ``list`` / ``str`` /
    ``int`` / ``float`` (finite) / ``bool`` / ``None``, so it can be compared
    with ``==`` and encoded with ``allow_nan=False``.
    """
    tracker = _Tracker()
    out = _convert(value, "", tracker, 0, set())
    return out, tracker.fallback


def to_config_dict(config: Any) -> dict[str, Any]:
    """Accept what ``init(config=...)`` may be given and return a plain dict.

    ``None`` is an empty config, an ``argparse.Namespace`` (or any object
    with a ``__dict__``-backed namespace) becomes ``vars()``, a dataclass
    instance becomes its fields, and any mapping becomes a ``dict``. The
    values themselves are left untouched: they are sanitised at send time,
    so ``run.config`` stays the caller's own objects.
    """
    if config is None:
        return {}
    if isinstance(config, argparse.Namespace):
        return dict(vars(config))
    if dataclasses.is_dataclass(config) and not isinstance(config, type):
        return {f.name: getattr(config, f.name) for f in dataclasses.fields(config)}
    if isinstance(config, Mapping):
        return dict(config)
    raise TypeError(
        f"config must be a mapping, an argparse.Namespace or a dataclass, "
        f"got {type(config).__name__}"
    )


def _join(path: str, key: str) -> str:
    return f"{path}.{key}" if path else key


def _is_numpy(value: Any) -> bool:
    module = type(value).__module__ or ""
    return module == "numpy" or module.startswith("numpy.")


def _float(value: float) -> float | str:
    if math.isnan(value):
        return "nan"
    if math.isinf(value):
        return "inf" if value > 0 else "-inf"
    return value


def _fallback(value: Any, path: str, tracker: _Tracker) -> str:
    tracker.fallback.append(path or "<config>")
    try:
        return str(value)
    except Exception:  # noqa: BLE001 - a broken __str__ must not break logging
        try:
            return repr(value)
        except Exception:  # noqa: BLE001
            return f"<unprintable {type(value).__name__}>"


def _convert(value: Any, path: str, tracker: _Tracker, depth: int, seen: set[int]) -> Any:
    # Plain JSON scalars first: by far the common case.
    if value is None or isinstance(value, (str, bool)):
        return value
    # Enum before int/float: an IntEnum *is* an int, but its name is what
    # the author wrote.
    if isinstance(value, enum.Enum):
        inner = _Tracker()
        converted = _convert(value.value, path, inner, depth + 1, seen)
        if inner.fallback:
            return value.name
        return converted
    if isinstance(value, int):
        return int(value)
    if isinstance(value, float):
        return _float(float(value))
    if isinstance(value, pathlib.PurePath):
        return str(value)
    if isinstance(value, (_dt.datetime, _dt.date, _dt.time)):
        return value.isoformat()

    if depth >= _MAX_DEPTH:
        return _fallback(value, path, tracker)

    if _is_numpy(value):
        converted = _convert_numpy(value, path, tracker, depth, seen)
        if converted is not _NOT_HANDLED:
            return converted

    container = isinstance(value, (Mapping, list, tuple, set, frozenset, argparse.Namespace)) or (
        dataclasses.is_dataclass(value) and not isinstance(value, type)
    )
    if container:
        marker = id(value)
        if marker in seen:
            return _fallback(value, path, tracker)
        seen.add(marker)
        try:
            return _convert_container(value, path, tracker, depth, seen)
        finally:
            seen.discard(marker)

    # Other real numbers (fractions.Fraction, a numpy scalar that slipped
    # past the checks above): keep the number when it converts cleanly.
    if isinstance(value, numbers.Integral):
        try:
            return int(value)
        except Exception:  # noqa: BLE001
            return _fallback(value, path, tracker)
    if isinstance(value, numbers.Real):
        try:
            return _float(float(value))
        except Exception:  # noqa: BLE001
            return _fallback(value, path, tracker)
    return _fallback(value, path, tracker)


_NOT_HANDLED = object()


def _convert_numpy(value: Any, path: str, tracker: _Tracker, depth: int, seen: set[int]) -> Any:
    shape = getattr(value, "shape", None)
    if shape is not None and len(shape) > 0 and hasattr(value, "tolist"):
        size = getattr(value, "size", None)
        if size is None:
            size = 1
            for dim in shape:
                size *= dim
        if size > MAX_ARRAY_ELEMENTS:
            dtype = getattr(value, "dtype", "?")
            return f"ndarray(shape={tuple(shape)}, dtype={dtype})"
        try:
            listed = value.tolist()
        except Exception:  # noqa: BLE001
            return _NOT_HANDLED
        return _convert(listed, path, tracker, depth + 1, seen)
    if hasattr(value, "item"):
        try:
            item = value.item()
        except Exception:  # noqa: BLE001
            return _NOT_HANDLED
        if _is_numpy(item):  # would recurse forever
            return _NOT_HANDLED
        return _convert(item, path, tracker, depth + 1, seen)
    return _NOT_HANDLED


def _convert_container(value: Any, path: str, tracker: _Tracker, depth: int, seen: set[int]) -> Any:
    if isinstance(value, argparse.Namespace):
        value = vars(value)
    elif dataclasses.is_dataclass(value) and not isinstance(value, type):
        # Field by field rather than dataclasses.asdict(): asdict deep-copies
        # every leaf, and a field holding a lock or an open file would make
        # the whole conversion fail instead of just that value.
        value = {f.name: getattr(value, f.name) for f in dataclasses.fields(value)}

    if isinstance(value, Mapping):
        out: dict[str, Any] = {}
        for key, item in value.items():
            name = _key(key)
            out[name] = _convert(item, _join(path, name), tracker, depth + 1, seen)
        return out

    items = [
        _convert(item, _join(path, str(index)), tracker, depth + 1, seen)
        for index, item in enumerate(value)
    ]
    if isinstance(value, (set, frozenset)):
        # A set has no order of its own; sort so that two sanitisations of an
        # unchanged config compare equal and the stored value is stable.
        try:
            items.sort(key=lambda item: json.dumps(item, sort_keys=True))
        except Exception:  # noqa: BLE001
            pass
    return items


def _key(key: Any) -> str:
    if isinstance(key, str):
        return key
    if isinstance(key, enum.Enum):
        return str(key.name)
    try:
        return str(key)
    except Exception:  # noqa: BLE001
        return repr(key)
