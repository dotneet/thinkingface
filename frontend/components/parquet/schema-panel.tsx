"use client";

import { useMemo, useState } from "react";
import { badgeClass } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/field";
import { FilterInput } from "@/components/ui/search-input";
import { useT } from "@/lib/i18n/client";
import type { ParquetColumn } from "@/types/api";

/**
 * The file's columns, with checkboxes to hide them from the Rows table.
 *
 * `onToggle` is optional: the SQL panel shows the same list as a reference
 * while you write a query, but column visibility there is decided by the
 * SELECT list, so the checkboxes would be lying about what they control.
 * `onSetHidden` (bulk show/hide) is gated the same way.
 */
export function SchemaPanel({
  columns,
  hidden,
  onToggle,
  onSetHidden,
}: {
  columns: ParquetColumn[];
  hidden: Set<string>;
  onToggle?: (name: string) => void;
  /**
   * Bulk show/hide for the "show all" / "hide all" buttons. `names` is
   * whatever the panel currently has in view — the filtered subset while a
   * filter is typed, all columns otherwise — so search-then-toggle only
   * touches the columns actually visible.
   */
  onSetHidden?: (names: string[], hide: boolean) => void;
}) {
  const t = useT();
  const interactive = onToggle !== undefined;
  const Row = interactive ? "label" : "div";
  const [filter, setFilter] = useState("");

  const filteredColumns = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return columns;
    return columns.filter((c) => c.name.toLowerCase().includes(q));
  }, [columns, filter]);

  const filteredNames = useMemo(() => filteredColumns.map((c) => c.name), [filteredColumns]);
  const shownCount = columns.length - hidden.size;

  return (
    <div className="flex w-full flex-col gap-1 lg:w-64 lg:shrink-0">
      {/* Header height stays constant regardless of filter text or match
          count (DESIGN.md §8-3): the filter input and the show/hide buttons
          are always rendered, never conditionally inserted, so the column
          list below never jumps. */}
      <div className="mb-1 flex flex-col gap-1.5 px-1">
        <div className="flex items-center justify-between">
          <span className="text-xs font-medium uppercase tracking-wide text-fg-subtle">
            {t("parquet.schema.title")}
          </span>
          <span className="text-xs font-medium tabular-nums text-fg-subtle">
            {t("parquet.schema.columnsShown", { shown: shownCount, total: columns.length })}
          </span>
        </div>
        <FilterInput
          value={filter}
          onChange={setFilter}
          placeholder={t("parquet.schema.filterPlaceholder")}
          className="py-1 pl-8 pr-2 text-xs"
        />
        <div className="flex gap-1.5">
          <Button
            size="sm"
            className="flex-1"
            disabled={!onSetHidden || filteredNames.length === 0}
            onClick={() => onSetHidden?.(filteredNames, false)}
          >
            {t("parquet.schema.showAll")}
          </Button>
          <Button
            size="sm"
            className="flex-1"
            disabled={!onSetHidden || filteredNames.length === 0}
            onClick={() => onSetHidden?.(filteredNames, true)}
          >
            {t("parquet.schema.hideAll")}
          </Button>
        </div>
      </div>
      <div className="scroll-x flex flex-col overflow-y-auto rounded-lg border border-border lg:max-h-[70vh]">
        {filteredColumns.length === 0 ? (
          <div className="px-2.5 py-3 text-xs font-medium text-fg-subtle">
            {t("parquet.schema.noMatches")}
          </div>
        ) : (
          filteredColumns.map((col) => (
            <Row
              key={col.name}
              className={`flex items-start gap-2 border-b border-border px-2.5 py-2 text-xs last:border-0 hover:bg-bg-hover${
                interactive ? " cursor-pointer" : ""
              }`}
            >
              {interactive && (
                <Checkbox
                  checked={!hidden.has(col.name)}
                  onChange={() => onToggle(col.name)}
                  className="mt-0.5"
                />
              )}
              <span className="min-w-0 flex-1">
                <span className="block truncate font-mono font-medium text-fg">{col.name}</span>
                <span className="flex flex-wrap items-center gap-1 text-fg-subtle">
                  <span>
                    {col.logical_type || col.type}
                    {col.optional ? ` · ${t("parquet.schema.nullable")}` : ""}
                    {col.repeated ? ` · ${t("parquet.schema.repeated")}` : ""}
                  </span>
                  {col.feature && (
                    <span
                      className={badgeClass({ tone: "accent" })}
                      title={t("parquet.schema.feature", { feature: col.feature })}
                    >
                      {col.feature}
                    </span>
                  )}
                </span>
              </span>
            </Row>
          ))
        )}
      </div>
    </div>
  );
}
