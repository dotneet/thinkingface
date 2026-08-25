"use client";

import { createContext, useContext } from "react";
import type { RunModels } from "@/lib/lineage";
import { metricSortColumn, type RunSortColumn } from "@/lib/run-grouping";
import type { ExpRun } from "@/types/api";

export type RunTableActions = {
  onEditTags: (run: ExpRun) => void;
  onToggleArchived: (run: ExpRun) => void;
  onToggleBaseline: (run: ExpRun) => void;
  /** Opens the delete confirmation for this run (the dialog lives above). */
  onDelete: (run: ExpRun) => void;
  /** Run currently being written to the backend, if any. */
  pendingRun?: string;
};

/**
 * One column of the run table.
 *
 * The header, the group rows and the run rows all render from the *same*
 * array, which is the point of it existing: the metric columns and the
 * checkpoint column are both conditional, and while each row kind decided for
 * itself how many cells to emit, adding a column meant editing three places
 * and getting a row one `<td>` short of its header was a silent misalignment
 * rather than an error.
 */
export type RunColumn =
  | { id: "select" | "name" | "status" | "tags" | "lastStep" | "started" | "models" | "actions" }
  | { id: "metric"; metric: string; sort: RunSortColumn };

/**
 * The columns this table draws, left to right. `metricKeys` is already capped
 * (`metricColumns` in `lib/run-grouping.ts`), and the checkpoint column only
 * appears once something in the project has declared a run as its origin —
 * otherwise every experiment table would carry a dead column.
 */
export function runColumns(metricKeys: string[], hasModels: boolean): RunColumn[] {
  return [
    { id: "select" },
    { id: "name" },
    { id: "status" },
    { id: "tags" },
    { id: "lastStep" },
    ...metricKeys.map(
      (metric): RunColumn => ({ id: "metric", metric, sort: metricSortColumn(metric) }),
    ),
    { id: "started" },
    ...(hasModels ? [{ id: "models" } satisfies RunColumn] : []),
    { id: "actions" },
  ];
}

/** React key for a column: unique because metric keys are unique among themselves. */
export function runColumnKey(column: RunColumn): string {
  return column.id === "metric" ? `metric:${column.metric}` : column.id;
}

type RunTableContextValue = {
  /** Identity of the project, so each row can link to its run detail page. */
  ns: string;
  repo: string;
  project: string;
  /** `runColorIndex(runOrder)`: colours stay put when the list is filtered. */
  colorIndex: ReadonlyMap<string, number>;
  columns: RunColumn[];
  /**
   * Checkpoint repositories each run produced, keyed by run name. A run's
   * models are the repositories whose card names it under `lineage.run`.
   */
  runModels?: RunModels;
  actions: RunTableActions;
};

/**
 * Everything a row needs that is the same for every row.
 *
 * These values used to be written out in three prop lists — the table's, the
 * group row's and the run row's — and passed down unchanged through both, so
 * a column that needed one more of them meant editing three signatures. A
 * context is the right shape for a value that is constant across the subtree
 * and only read at the bottom of it.
 */
const RunTableContext = createContext<RunTableContextValue | null>(null);

export const RunTableProvider = RunTableContext.Provider;

export function useRunTable(): RunTableContextValue {
  const value = useContext(RunTableContext);
  if (value === null) {
    throw new Error("useRunTable must be used inside a RunTableProvider");
  }
  return value;
}
