"use client";

import { ArrowDown, ArrowUp, ChevronsUpDown } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import type { RunSort, RunSortColumn } from "@/lib/run-grouping";

/** A column header that sorts the table, showing the current direction. */
export function SortHeader({
  column,
  label,
  mono = false,
  sort,
  onSort,
  ariaLabel,
}: {
  column: RunSortColumn;
  label: string;
  /** Metric columns are named by their key, which is an identifier. */
  mono?: boolean;
  sort: RunSort | null;
  onSort: (column: RunSortColumn) => void;
  ariaLabel: string;
}) {
  const active = sort?.column === column;
  return (
    <Button
      size="sm"
      variant="ghost"
      onClick={() => onSort(column)}
      aria-label={ariaLabel}
      aria-pressed={active}
      className={cn("px-1 py-0.5 text-xs font-medium", mono && "font-mono")}
    >
      {label}
      {active ? (
        sort?.dir === "asc" ? (
          <ArrowUp size={12} className="text-accent" />
        ) : (
          <ArrowDown size={12} className="text-accent" />
        )
      ) : (
        // Same slot, always rendered: a column that can be sorted but isn't
        // currently active still reserves the icon's width, so clicking a
        // header never shifts every column to its right (see DESIGN.md §8).
        <ChevronsUpDown size={12} className="text-fg-subtle opacity-50" />
      )}
    </Button>
  );
}
