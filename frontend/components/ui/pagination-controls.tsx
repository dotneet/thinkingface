"use client";

import { Inbox } from "lucide-react";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import type { PagerState } from "@/hooks/use-paged-list";
import { formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";

/**
 * The Client counterpart to `ui/pagination.tsx`, which is a Server Component
 * that paginates by href. Here the offset lives in component state and the
 * controls are buttons, which is the shape every `usePagedList` screen needs
 * (`hooks/use-paged-list.ts`).
 *
 * Both halves of the pair render `ui.pagination.*`, so the range line and the
 * Prev/Next labels read the same whichever kind of listing you are looking at.
 *
 * The window arrives as the hook's `pager` rather than prop by prop: this
 * component and the hook used to spell out the same conditions separately,
 * which is only correct while nobody edits one of them (see `PagerState`).
 */
export function PaginationControls({ pager }: { pager: PagerState }) {
  const t = useT();
  // `outOfRange` is decided by the hook: past the end of a non-empty list,
  // `OutOfRangeEmptyState` is showing instead and it carries the way back.
  // Rendering a range here as well would print a `to` smaller than its `from`
  // (DESIGN.md §9).
  const { offset, pageSize, total, loadedCount, outOfRange, loading, onOffsetChange } = pager;
  const hasPrev = offset > 0;
  const hasNext = total !== null && offset + pageSize < total;

  if (outOfRange || !(hasPrev || hasNext)) return null;

  return (
    <div className="flex items-center justify-between text-sm text-fg-subtle">
      {/* A failed read leaves `total` null. Rendering it as 0 would put
          "51–0 of 0" directly under the error state, which reads as "the list
          is empty" rather than "we could not ask" (DESIGN.md §9). The buttons
          stay, because paging back is how you recover. */}
      <span className="tabular-nums">
        {total === null || loadedCount === null
          ? "—"
          : t("ui.pagination.range", {
              from: formatNumber(offset + 1),
              // From what actually arrived, not from the window's width: the
              // count and the page are separate reads, so a short last page or
              // a list that changed between them would otherwise be described
              // by a number no row backs up.
              to: formatNumber(offset + loadedCount),
              total: formatNumber(total),
            })}
      </span>
      <div className="flex gap-2">
        <Button
          size="sm"
          disabled={!hasPrev || loading}
          onClick={() => onOffsetChange(Math.max(0, offset - pageSize))}
        >
          {t("ui.pagination.prev")}
        </Button>
        <Button
          size="sm"
          disabled={!hasNext || loading}
          onClick={() => onOffsetChange(offset + pageSize)}
        >
          {t("ui.pagination.next")}
        </Button>
      </div>
    </div>
  );
}

/**
 * "This page is empty" is a different answer from "there is nothing here", and
 * paging is what makes the difference reachable: deleting the last row of the
 * last page leaves the window past the end of a list that is not empty at all
 * (DESIGN.md §9). Two listings used to fall through to their own "nothing
 * here" copy in exactly that situation and state something untrue.
 */
export function OutOfRangeEmptyState({ onBackToFirstPage }: { onBackToFirstPage: () => void }) {
  const t = useT();
  return (
    <EmptyState
      icon={Inbox}
      title={t("ui.pagination.outOfRangeTitle")}
      description={t("ui.pagination.outOfRangeDescription")}
      action={
        <Button size="sm" onClick={onBackToFirstPage}>
          {t("ui.pagination.backToFirstPage")}
        </Button>
      }
    />
  );
}
