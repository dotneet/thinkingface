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

/** Hard cap on how much raw text ever lands in the DOM for one cell. */
const DISPLAY_CAP = 80;

/**
 * The threshold for offering the click-to-expand modal. This is deliberately
 * *not* DISPLAY_CAP: the non-interactive rendering is `max-w-[320px]
 * truncate` at `text-sm`, which fits roughly 43 Latin characters before the
 * browser's own CSS ellipsis clips it — silently, with no `title` and (before
 * this fix) no way to reach the rest of the value except copying the whole
 * table as CSV. Gating the modal on a character count that matches
 * DISPLAY_CAP left every 43-80 character value clipped with no escape hatch.
 * Kept comfortably under the ~43-character visual capacity (rather than
 * exactly at it) to absorb font-metric variance across browsers/OSes and CJK
 * glyphs, which are wider per character than Latin ones.
 */
const INTERACTIVE_LEN = 40;

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

  // `null` and "this row has no such key" are two different claims, and
  // collapsing them (both are falsy, both used to print the literal "null")
  // is exactly DESIGN.md §9. A row that simply does not carry the column —
  // a jsonl object missing a key, or a page fetched before the column set
  // caught up — gets the em dash this app uses everywhere for "absent",
  // while a genuine null still says so. The dash is punctuation, not copy,
  // so it needs no dictionary entry.
  if (value === undefined) {
    return <span className="text-fg-subtle">—</span>;
  }
  if (value === null) {
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
          {/* An `external` src is a third-party URL the dataset chose: no
              referrer, so that server is never told which repository,
              revision and file the reader is looking at. */}
          {/* eslint-disable-next-line @next/next/no-img-element -- next/image cannot optimise a data: URL, which is what an inlined `datasets` Image column is. */}
          <img
            src={image.src}
            alt={alt}
            loading="lazy"
            decoding="async"
            referrerPolicy={image.external ? "no-referrer" : undefined}
            onError={() => setFailedSrc(image.src)}
            className="h-8 max-w-[160px] rounded border border-border bg-bg-sunken object-contain"
          />
        </Button>
        {open && <CellModal value={value} feature={feature} onClose={() => setOpen(false)} />}
      </>
    );
  }

  const isTruncated = text.length > INTERACTIVE_LEN;
  const display = text.length > DISPLAY_CAP ? `${text.slice(0, DISPLAY_CAP)}…` : text;
  // A nested value earns the modal at any length: the tree is a better reading
  // of `{"a":{"b":1}}` than the one-line JSON is, truncated or not.
  const hasTree = jsonTreeValueFor(value, feature) !== undefined;

  // A scalar that fits within INTERACTIVE_LEN has nothing for the modal to
  // reveal, so it renders as plain, non-interactive text — keeping it a real
  // <button> regardless of truncation would put a "does nothing" stop on
  // every cell in a keyboard tab order and a screen reader's "button"
  // announcement, which gets expensive fast in a table with hundreds of
  // visible cells. `title` is set regardless: at exactly the threshold, font
  // metrics can still clip a character or two that the length check alone
  // would miss, and a hover tooltip is free insurance against that.
  if (!isTruncated && !hasTree) {
    return (
      <span className="block max-w-[320px] truncate text-sm font-normal text-fg" title={text}>
        {display}
      </span>
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
