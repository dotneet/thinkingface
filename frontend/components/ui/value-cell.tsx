"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { CellModal } from "@/components/ui/cell-modal";
import {
  type CellFeature,
  imageSourceFor,
  jsonTreeValueFor,
  stringifyValue,
} from "@/lib/cell-value";
import { useT } from "@/lib/i18n/client";

const MAX_LEN = 80;

export function ValueCell({ value, feature }: { value: unknown; feature?: CellFeature }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  // A payload the browser refuses to decode (an unsniffable blob, a broken
  // remote URL) is only discovered at load time, so the thumbnail swaps itself
  // for text rather than leaving a broken-image glyph in the row. Keyed by
  // src rather than a bare boolean: DataTable keys rows by index, so paging
  // hands this same component a new value, which must not inherit the
  // previous image's failure.
  const [failedSrc, setFailedSrc] = useState<string | null>(null);

  if (value === null || value === undefined) {
    return <span className="text-fg-subtle">null</span>;
  }

  const image = feature === "image" ? imageSourceFor(value) : null;
  const text = stringifyValue(value);

  if (image !== null) {
    if (failedSrc === image.src) {
      return (
        <span className="block max-w-[320px] truncate text-sm text-fg-subtle">
          {image.path ?? t("ui.cell.imageUnavailable")}
        </span>
      );
    }
    const alt = image.path ?? t("ui.cell.image");
    return (
      <>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => setOpen(true)}
          className="block cursor-pointer rounded-none px-0 py-0 hover:bg-transparent"
          title={t("ui.cell.viewImage")}
        >
          {/* eslint-disable-next-line @next/next/no-img-element -- next/image cannot optimise a data: URL, which is what an inlined `datasets` Image column is. */}
          <img
            src={image.src}
            alt={alt}
            loading="lazy"
            decoding="async"
            onError={() => setFailedSrc(image.src)}
            className="h-8 max-w-[160px] rounded border border-border bg-bg-sunken object-contain"
          />
        </Button>
        {open && <CellModal value={value} feature={feature} onClose={() => setOpen(false)} />}
      </>
    );
  }

  const isTruncated = text.length > MAX_LEN;
  const display = isTruncated ? `${text.slice(0, MAX_LEN)}…` : text;
  // A nested value earns the modal at any length: the tree is a better reading
  // of `{"a":{"b":1}}` than the one-line JSON is, truncated or not.
  const hasTree = jsonTreeValueFor(value, feature) !== undefined;

  // A scalar that fits within MAX_LEN has nothing for the modal to reveal, so
  // it renders as plain, non-interactive text — keeping it a real <button>
  // regardless of truncation would put a "does nothing" stop on every cell
  // in a keyboard tab order and a screen reader's "button" announcement,
  // which gets expensive fast in a table with hundreds of visible cells.
  if (!isTruncated && !hasTree) {
    return (
      <span className="block max-w-[320px] truncate text-sm font-normal text-fg">{display}</span>
    );
  }

  return (
    <>
      <Button
        variant="ghost"
        size="sm"
        onClick={() => setOpen(true)}
        className="block max-w-[320px] cursor-pointer truncate rounded-none px-0 py-0 text-left text-sm font-normal text-fg hover:bg-transparent hover:underline"
        title={t("ui.viewFullValue")}
      >
        {display}
      </Button>
      {open && <CellModal value={value} feature={feature} onClose={() => setOpen(false)} />}
    </>
  );
}
