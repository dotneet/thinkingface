"""``trackio.Image`` / ``trackio.Table``: rich values logged alongside metrics.

A value of either type passed to ``trackio.log()`` is not a metric: it is
pulled out of the point, written to a file, and committed as a run artifact
(docs/dev/agent-features.md §2.8)::

    trackio.log({"samples": trackio.Image("out/grid.png")}, step=10)
    #   -> {project}/artifacts/{run}/media/samples/step_00000010.png
    trackio.log({"preds": trackio.Table(dataframe=df)}, step=10)
    #   -> {project}/artifacts/{run}/tables/preds/step_00000010.parquet

Every encoder here is optional-dependency aware: Pillow is needed only to
encode something that is not already an image file, and a table needs
pyarrow (a dependency of this package) or pandas. A missing library is a
``MediaError`` with ``missing_dependency`` set, which the run turns into one
warning rather than an exception.
"""

from __future__ import annotations

import os
import shutil
from pathlib import Path
from typing import Any


class MediaError(Exception):
    """A media value could not be written. Never escapes ``trackio.log()``."""

    def __init__(self, message: str, missing_dependency: str | None = None) -> None:
        super().__init__(message)
        self.missing_dependency = missing_dependency


class Image:
    """An image logged as a metric value.

    ``value`` is a path to an existing image file, a ``PIL.Image.Image``, or a
    numpy array of shape HxW or HxWxC (uint8, or floats in ``[0, 1]``). A PNG
    file is committed as is; anything else is encoded to PNG with Pillow. A
    non-PNG file with no Pillow installed is committed in its own format
    under its own extension rather than dropped. ``caption`` is stored in the
    PNG's text metadata when Pillow does the encoding.
    """

    def __init__(self, value: Any, caption: str | None = None) -> None:
        self.value = value
        self.caption = caption

    def __repr__(self) -> str:
        return f"Image({type(self.value).__name__}, caption={self.caption!r})"


class Table:
    """A table logged as a metric value, stored as parquet.

    Pass either ``dataframe`` (a pandas DataFrame or a ``pyarrow.Table``) or
    ``data`` -- a list of rows, each a sequence matching ``columns`` or a
    mapping of column name to value.
    """

    def __init__(
        self,
        dataframe: Any = None,
        columns: list[str] | None = None,
        data: list[Any] | None = None,
    ) -> None:
        if dataframe is None and data is None:
            raise ValueError("Table needs either dataframe= or data=")
        self.dataframe = dataframe
        self.columns = list(columns) if columns is not None else None
        self.data = list(data) if data is not None else None

    def __repr__(self) -> str:
        if self.dataframe is not None:
            return f"Table(dataframe={type(self.dataframe).__name__})"
        return f"Table(columns={self.columns!r}, rows={len(self.data or [])})"


def kind(value: Any) -> tuple[str, str] | None:
    """``(directory, extension)`` for a media value, or None for a metric."""
    if isinstance(value, Image):
        return "media", "png"
    if isinstance(value, Table):
        return "tables", "parquet"
    return None


def write(value: Image | Table, target: Path) -> Path:
    """Write ``value`` to ``target`` (or a sibling with another extension,
    returned) and return the path actually written."""
    if isinstance(value, Image):
        return _write_image(value, target)
    _write_table(value, target)
    return target


# -- images -----------------------------------------------------------------


def _pil_image_module() -> Any:
    try:
        from PIL import Image as pil_image  # type: ignore[import-not-found]
    except ImportError:
        return None
    return pil_image


def _is_pil_image(value: Any) -> bool:
    module = type(value).__module__ or ""
    return module.startswith("PIL") and hasattr(value, "save")


def _save_png(image: Any, target: Path, caption: str | None) -> None:
    kwargs: dict[str, Any] = {"format": "PNG"}
    if caption:
        try:
            from PIL.PngImagePlugin import PngInfo  # type: ignore[import-not-found]

            info = PngInfo()
            info.add_text("Caption", caption)
            kwargs["pnginfo"] = info
        except Exception:  # noqa: BLE001 - the caption is a nicety
            pass
    image.save(str(target), **kwargs)


def _write_image(image: Image, target: Path) -> Path:
    value = image.value
    if isinstance(value, (str, os.PathLike)):
        source = Path(value)
        if not source.is_file():
            raise MediaError(f"image file {source} does not exist")
        suffix = source.suffix.lower()
        if suffix == ".png":
            shutil.copyfile(source, target)
            return target
        pil = _pil_image_module()
        if pil is None:
            # Committed as what it is rather than dropped: the server does
            # not care about the extension, and the repository browser shows
            # a JPEG just as well.
            written = target.with_suffix(suffix or ".bin")
            shutil.copyfile(source, written)
            return written
        with pil.open(source) as opened:
            _save_png(opened, target, image.caption)
        return target
    if _is_pil_image(value):
        _save_png(value, target, image.caption)
        return target
    if hasattr(value, "shape") and hasattr(value, "dtype"):
        pil = _pil_image_module()
        if pil is None:
            raise MediaError(
                "encoding an array as PNG needs Pillow (pip install pillow)",
                missing_dependency="Pillow",
            )
        _save_png(pil.fromarray(_to_uint8(value)), target, image.caption)
        return target
    raise MediaError(
        f"Image() takes a file path, a PIL image or a numpy array, got {type(value).__name__}"
    )


def _to_uint8(array: Any) -> Any:
    """HxW / HxWxC array -> uint8, scaling floats in [0, 1] to [0, 255]."""
    import numpy as np  # present: the value is a numpy array

    data = np.asarray(array)
    if data.ndim == 3 and data.shape[-1] == 1:
        data = data[..., 0]
    if data.ndim not in (2, 3):
        raise MediaError(f"an image array must be HxW or HxWxC, got shape {data.shape}")
    if data.dtype == np.uint8:
        return data
    if data.dtype.kind == "b":
        return data.astype(np.uint8) * 255
    if data.dtype.kind == "f":
        finite = data[np.isfinite(data)]
        if finite.size and float(finite.max()) <= 1.0:
            data = data * 255.0
        data = np.nan_to_num(data, nan=0.0, posinf=255.0, neginf=0.0)
    return np.clip(data, 0, 255).astype(np.uint8)


# -- tables -------------------------------------------------------------------


def _write_table(table: Table, target: Path) -> None:
    try:
        import pyarrow as pa
        import pyarrow.parquet as pq
    except ImportError:
        pa = None
    if pa is not None:
        pq.write_table(_to_arrow(table, pa), str(target))
        return

    try:
        import pandas as pd  # type: ignore[import-not-found]
    except ImportError:
        raise MediaError(
            "writing a Table needs pyarrow or pandas (pip install pyarrow)",
            missing_dependency="pyarrow",
        ) from None
    frame = table.dataframe
    if frame is None:
        frame = pd.DataFrame(table.data, columns=table.columns)
    try:
        frame.to_parquet(str(target), index=False)
    except ImportError as exc:  # pandas without a parquet engine
        raise MediaError(
            f"writing a Table needs a parquet engine ({exc})", missing_dependency="pyarrow"
        ) from None


def _to_arrow(table: Table, pa: Any) -> Any:
    frame = table.dataframe
    if frame is not None:
        if isinstance(frame, pa.Table):
            return frame
        if hasattr(frame, "to_arrow") and not hasattr(frame, "iloc"):  # e.g. polars
            return frame.to_arrow()
        return pa.Table.from_pandas(frame, preserve_index=False)
    rows = table.data or []
    if rows and all(isinstance(row, dict) for row in rows):
        arrow = pa.Table.from_pylist(rows)
        if table.columns:
            arrow = arrow.select(table.columns)
        return arrow
    width = max((len(row) for row in rows), default=len(table.columns or []))
    columns = table.columns or [f"col{i}" for i in range(width)]
    for row in rows:
        if len(row) != len(columns):
            raise MediaError(
                f"a Table row has {len(row)} values but there are {len(columns)} columns"
            )
    return pa.table({name: [row[i] for row in rows] for i, name in enumerate(columns)})
