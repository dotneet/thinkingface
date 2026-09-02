"use client";

import {
  createColumnHelper,
  flexRender,
  getCoreRowModel,
  useReactTable,
} from "@tanstack/react-table";
import { useVirtualizer } from "@tanstack/react-virtual";
import { useEffect, useMemo, useRef } from "react";
import { ValueCell } from "@/components/ui/value-cell";
import type { CellFeature } from "@/lib/cell-value";
import { cn } from "@/lib/cn";

export type DataTableRow = Record<string, unknown>;

export type DataTableColumn = {
  /** Key into each row object. */
  key: string;
  /** Header label; defaults to `key`. */
  label?: string;
  /** Second header line — an Arrow/Parquet type, for instance. */
  hint?: string;
  /**
   * Rendering hint for the column's cells (image thumbnails, JSON tree …).
   * See lib/cell-value.ts; undefined renders values as text.
   */
  feature?: CellFeature;
};

/**
 * Virtualized, horizontally scrollable grid of `Record<string, unknown>` rows.
 *
 * Extracted from the Parquet viewer so the SQL console and the CSV/JSONL
 * preview render identically: same sticky header, same row height, same
 * click-to-expand cells. Only the rows in view are mounted, with spacer rows
 * above and below, so a 50k-row CSV costs the same as a 50-row one.
 *
 * The caller owns paging and sorting; this component only draws what it is
 * given, and renders nothing when `columns` is empty (callers already have a
 * meaningful EmptyState for that case). It owns the scroll box, though, so a
 * caller that replaces the rows wholesale passes `scrollResetKey` — see the
 * prop.
 */
export function DataTable({
  columns,
  rows,
  rowHeight = 39,
  minColumnWidth = 140,
  className,
  scrollResetKey,
}: {
  columns: DataTableColumn[];
  rows: DataTableRow[];
  rowHeight?: number;
  minColumnWidth?: number;
  /** Overrides the default `max-h-[70vh]` scroll box. */
  className?: string;
  /**
   * Identifies *which* rows these are (a page offset, a query). When it
   * changes the scroll box jumps back to the top, because the rows underneath
   * are a different set: a 100-row page is ~3900px tall, so paging while
   * scrolled down used to open the next page halfway through it, with the
   * first few dozen rows already scrolled past and never seen. Leave unset
   * for a table whose rows only ever grow in place.
   */
  scrollResetKey?: string | number;
}) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const columnHelper = useMemo(() => createColumnHelper<DataTableRow>(), []);

  const tableColumns = useMemo(
    () =>
      columns.map((col) =>
        columnHelper.accessor((row) => row[col.key], {
          id: col.key,
          header: () => (
            <div className="flex flex-col">
              <span>{col.label ?? col.key}</span>
              {col.hint && <span className="text-fg-subtle">{col.hint}</span>}
            </div>
          ),
          cell: (info) => <ValueCell value={info.getValue()} feature={col.feature} />,
        }),
      ),
    [columns, columnHelper],
  );

  const table = useReactTable({
    data: rows,
    columns: tableColumns,
    getCoreRowModel: getCoreRowModel(),
  });

  const tableRows = table.getRowModel().rows;
  const virtualizer = useVirtualizer({
    count: tableRows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => rowHeight,
    overscan: 12,
  });

  // Scrolling the element (rather than only the virtualizer) is what the
  // virtualizer itself listens to, so both end up at the top.
  useEffect(() => {
    if (scrollResetKey === undefined) return;
    const el = scrollRef.current;
    if (el) el.scrollTop = 0;
  }, [scrollResetKey]);

  const virtualRows = virtualizer.getVirtualItems();
  const paddingTop = virtualRows.length > 0 ? virtualRows[0]!.start : 0;
  const paddingBottom =
    virtualRows.length > 0
      ? virtualizer.getTotalSize() - virtualRows[virtualRows.length - 1]!.end
      : 0;
  const colSpan = Math.max(tableColumns.length, 1);

  return (
    <div
      ref={scrollRef}
      className={cn(
        "scroll-x overflow-y-auto rounded-lg border border-border",
        className ?? "max-h-[70vh]",
      )}
    >
      {/* text-sm, not text-xs: these cells are the dataset itself, the thing
          the reader came to read, not metadata around it. Only the header row
          below stays at text-xs. */}
      <table className="w-full min-w-max border-collapse text-sm tabular-nums">
        <thead className="sticky top-0 z-10 bg-bg-sunken">
          {table.getHeaderGroups().map((hg) => (
            <tr key={hg.id}>
              {hg.headers.map((header) => (
                <th
                  key={header.id}
                  scope="col"
                  className="whitespace-nowrap border-b border-border px-3 py-2 text-left font-mono text-xs font-medium text-fg-muted"
                  style={{ minWidth: minColumnWidth }}
                >
                  {flexRender(header.column.columnDef.header, header.getContext())}
                </th>
              ))}
            </tr>
          ))}
        </thead>
        <tbody>
          {paddingTop > 0 && (
            <tr>
              <td colSpan={colSpan} style={{ height: paddingTop, padding: 0, border: 0 }} />
            </tr>
          )}
          {virtualRows.map((vRow) => {
            const row = tableRows[vRow.index];
            if (!row) return null;
            return (
              <tr key={row.id} className="border-b border-border hover:bg-bg-hover">
                {row.getVisibleCells().map((cell) => (
                  <td key={cell.id} className="px-3 py-2" style={{ minWidth: minColumnWidth }}>
                    {flexRender(cell.column.columnDef.cell, cell.getContext())}
                  </td>
                ))}
              </tr>
            );
          })}
          {paddingBottom > 0 && (
            <tr>
              <td colSpan={colSpan} style={{ height: paddingBottom, padding: 0, border: 0 }} />
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
