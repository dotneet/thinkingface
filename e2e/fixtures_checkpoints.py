"""Byte-level checkpoint fixtures for the model-meta / web-edit e2e tests.

Both `test_model_meta.py` and `test_web_edit.py` need real checkpoint bytes
to upload: a safetensors file and a PyTorch `torch.save` archive. Building
the safetensors one is trivial (it's just a JSON header + padding, see
`safetensors_file()` below). The PyTorch one is the interesting case.

A `torch.save` file is a zip archive whose `archive/data.pkl` member is a
*pickle* of the checkpoint object graph, with tensors represented via
`persistent_id` (for the underlying storage) and a
`torch._utils._rebuild_tensor_v2` reduce call (for shape/stride/dtype). None
of that actually requires PyTorch to produce -- it only requires that the
pickle opcodes reference callables/classes named `torch.FloatStorage`,
`torch._utils._rebuild_tensor_v2`, etc. by *module path and qualname*, which
is exactly what `pickle.Pickler` writes when you hand it real objects whose
`__module__`/`__qualname__` are set accordingly.

So instead of installing the (very large, GPU-toolchain-adjacent) `torch`
package as a test dependency just to call `torch.save`, this module
registers small stand-in classes/functions under fake `torch` /
`torch._utils` entries in `sys.modules`, gives them the right dunders, and
drives CPython's own `pickle.Pickler` directly (including a
`persistent_id` hook, the same mechanism `torch.save` uses for storages).
The resulting opcode stream has the same shape a real `torch.save` would
produce, so the backend's checkpoint reader (which only ever parses the
pickle -- it never imports or executes anything from it) can't tell the
difference. This keeps `e2e/pyproject.toml` free of a torch dependency.
"""

from __future__ import annotations

import collections
import contextlib
import io
import json
import pickle
import struct
import sys
import threading
import types
import zipfile

# --- fake `torch` / `torch._utils` modules, just enough for the pickler ----

_torch_mod = types.ModuleType("torch")
_utils_mod = types.ModuleType("torch._utils")


# `sys.modules` is process-global, so two threads pickling at once would see
# each other's install/restore. An RLock keeps the swap atomic and still allows
# the nested/re-entrant case (restoring to the fake, then to the original).
_fake_torch_lock = threading.RLock()


@contextlib.contextmanager
def _fake_torch_installed():
    """Install the stand-in modules only while pickling.

    pickle verifies a GLOBAL by looking the name back up in sys.modules and
    comparing identity, so the fakes must be present during `dump` (and a real
    torch left in place would make it reject the stand-ins). But leaving them
    in sys.modules poisons everyone else: the fake has `__spec__ = None`, so a
    later `importlib.util.find_spec("torch")` -- which `datasets` runs on
    import -- raises ValueError. Install for the duration of the dump, then
    restore whatever was there before.
    """
    with _fake_torch_lock:
        saved = {name: sys.modules.get(name) for name in ("torch", "torch._utils")}
        sys.modules["torch"] = _torch_mod
        sys.modules["torch._utils"] = _utils_mod
        try:
            yield
        finally:
            for name, mod in saved.items():
                if mod is None:
                    # Another (nested) caller may already have removed it.
                    sys.modules.pop(name, None)
                else:
                    sys.modules[name] = mod


for _cls_name in ("FloatStorage", "HalfStorage", "LongStorage", "BFloat16Storage"):
    _cls = type(_cls_name, (), {})
    _cls.__module__ = "torch"
    _cls.__qualname__ = _cls_name
    setattr(_torch_mod, _cls_name, _cls)


def _rebuild_tensor_v2(*args):  # pragma: no cover - never actually called
    raise NotImplementedError("stand-in for torch._utils._rebuild_tensor_v2")


_rebuild_tensor_v2.__module__ = "torch._utils"
_rebuild_tensor_v2.__qualname__ = "_rebuild_tensor_v2"
_utils_mod._rebuild_tensor_v2 = _rebuild_tensor_v2
_torch_mod._utils = _utils_mod


class _FakeStorage:
    """Stand-in for a `torch.Storage`: just enough for `persistent_id`."""

    def __init__(self, cls, key: str, numel: int) -> None:
        self.cls, self.key, self.numel = cls, key, numel


class _FakeTensor:
    """Stand-in for a `torch.Tensor`: reduces via `_rebuild_tensor_v2`,
    exactly like the real thing does when pickled."""

    def __init__(
        self, storage: _FakeStorage, size: tuple[int, ...], stride: tuple[int, ...]
    ) -> None:
        self.storage, self.size, self.stride = storage, size, stride

    def __reduce_ex__(self, protocol):
        return (
            _rebuild_tensor_v2,
            (self.storage, 0, self.size, self.stride, False, collections.OrderedDict()),
        )


class _TorchPickler(pickle.Pickler):
    """Mirrors `torch.save`'s use of `persistent_id` to keep storages out of
    the main pickle stream (they instead become zip members)."""

    def persistent_id(self, obj):
        if isinstance(obj, _FakeStorage):
            return ("storage", obj.cls, obj.key, "cpu", obj.numel)
        return None


def torch_checkpoint() -> bytes:
    """A `torch.save`-shaped zip archive with a small, varied state_dict.

    Layout mirrors a typical training checkpoint: a `state_dict` with mixed
    dtypes (float32, float16) including a 0-dimensional tensor, plus scalar
    training metadata (`epoch`, `global_step`, `arch`) alongside it -- both
    of which `test_model_meta.py` asserts on.
    """
    state = collections.OrderedDict()
    state["encoder.weight"] = _FakeTensor(
        _FakeStorage(_torch_mod.FloatStorage, "0", 32), (4, 8), (8, 1)
    )
    state["encoder.bias"] = _FakeTensor(_FakeStorage(_torch_mod.FloatStorage, "1", 4), (4,), (1,))
    state["head.weight"] = _FakeTensor(
        _FakeStorage(_torch_mod.HalfStorage, "2", 12), (3, 4), (4, 1)
    )
    # A 0-dimensional tensor (e.g. a scalar step counter kept as a tensor),
    # to exercise the empty-shape case.
    state["step_counter"] = _FakeTensor(_FakeStorage(_torch_mod.LongStorage, "3", 1), (), ())
    obj = {"state_dict": state, "epoch": 7, "global_step": 1234, "arch": "tiny-mlp"}

    buf = io.BytesIO()
    with _fake_torch_installed():
        _TorchPickler(buf, protocol=2).dump(obj)
    pkl = buf.getvalue()

    out = io.BytesIO()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_STORED) as zf:
        zf.writestr("archive/data.pkl", pkl)
        zf.writestr("archive/version", "3\n")
        # Storage payloads: content doesn't matter, only that the sizes are
        # plausible for the declared element counts (the reader never
        # touches these bytes -- only the header/pickle is parsed).
        for key, size in (("0", 128), ("1", 16), ("2", 24), ("3", 8)):
            zf.writestr(f"archive/data/{key}", b"\0" * size)
    return out.getvalue()


def safetensors_file() -> bytes:
    """A minimal, valid safetensors file: a JSON header (with `__metadata__`
    plus 3 tensors of mixed dtype) followed by padding bytes standing in for
    the tensor data. Tensor data content doesn't matter -- the model-meta
    endpoint only ever reads the header.
    """
    header = {
        "__metadata__": {"format": "pt", "modelspec.title": "tiny-mlp", "note": "x" * 200},
        "encoder.weight": {"dtype": "F32", "shape": [4, 8], "data_offsets": [0, 128]},
        "encoder.bias": {"dtype": "F32", "shape": [4], "data_offsets": [128, 144]},
        "head.weight": {"dtype": "BF16", "shape": [3, 4], "data_offsets": [144, 168]},
    }
    raw = json.dumps(header).encode("utf-8")
    return struct.pack("<Q", len(raw)) + raw + b"\0" * 168
