"use client";

import { useMemo, useState } from "react";
import { CsvDownloadButton } from "@/components/experiments/csv-download-button";
import { csvFilename, runTableCsv } from "@/components/experiments/run-csv";
import { GroupRows } from "@/components/experiments/run-group-rows";
import { RunRow } from "@/components/experiments/run-row";
import {
  type RunColumn,
  type RunTableActions,
  RunTableProvider,
  runColumnKey,
  runColumns,
} from "@/components/experiments/run-table-context";
import { SortHeader } from "@/components/experiments/sort-header";
import { Table, TBody, THead, Th } from "@/components/ui/table";
import { TriStateCheckbox } from "@/components/ui/tri-state-checkbox";
import { runColorIndex } from "@/lib/chart-utils";
import { useT } from "@/lib/i18n/client";
import type { RunModels } from "@/lib/lineage";
import {
  groupRuns,
  hiddenMetricCount,
  metricColumns,
  type RunSort,
  type RunSortColumn,
  sortGroups,
} from "@/lib/run-grouping";
import type { ExpRun } from "@/types/api";

/**
 * The run list.
 *
 * Runs that declared `trackio.init(group=...)` fold into one row per sweep —
 * a member count and the best value each metric reached inside it — which is
 * the difference between reading a forty-run sweep and scrolling past it. A
 * run that declared no group is a top-level row exactly as before, so a
 * project that never used grouping looks unchanged.
 *
 * Sorting and the metric filter live above (the dashboard owns them), because
 * the same filtered set feeds the charts.
 *
 * The rows themselves are `RunRow` / `GroupRows`, and everything they share —
 * the project identity, the colour order, the column list, the checkpoint
 * index, the action handlers — reaches them through `RunTableProvider` rather
 * than through both signatures on the way down.
 */
export function RunTable({
  ns,
  repo,
  project,
  runs,
  runOrder,
  selected,
  onToggle,
  onToggleAll,
  onToggleMany,
  runModels,
  actions,
  sort,
  onSort,
}: {
  /** Identity of the project, so each row can link to its run detail page. */
  ns: string;
  repo: string;
  project: string;
  runs: ExpRun[];
  /** Full project run order, so colours stay put when the list is filtered. */
  runOrder: string[];
  selected: Set<string>;
  onToggle: (name: string) => void;
  onToggleAll: () => void;
  /** Select or deselect a whole group at once. */
  onToggleMany: (names: string[], select: boolean) => void;
  /**
   * Checkpoint repositories each run produced, keyed by run name. A run's
   * models are the repositories whose card names it under `lineage.run`.
   */
  runModels?: RunModels;
  actions: RunTableActions;
  /** Current sort, or null for the order the server returned. */
  sort: RunSort | null;
  onSort: (column: RunSortColumn) => void;
}) {
  const t = useT();
  // Groups start folded: folding forty sweep runs into one row is the whole
  // point, and every group can be opened in one click.
  const [expanded, setExpanded] = useState<Set<string>>(new Set());

  const allSelected = runs.length > 0 && runs.every((r) => selected.has(r.name));
  const someSelected = !allSelected && runs.some((r) => selected.has(r.name));
  const groups = useMemo(() => sortGroups(groupRuns(runs), sort), [runs, sort]);
  const metricKeys = useMemo(() => metricColumns(runs), [runs]);
  const hiddenMetrics = useMemo(() => hiddenMetricCount(runs), [runs]);
  // The column only appears once something in this project has declared a run
  // as its origin; otherwise every experiment table would carry a dead column.
  const hasModels = useMemo(
    () => Object.values(runModels ?? {}).some((models) => models.length > 0),
    [runModels],
  );
  const columns = useMemo(() => runColumns(metricKeys, hasModels), [metricKeys, hasModels]);
  // One lookup built per render of the table rather than a scan of the whole
  // project per coloured dot (see runColorIndex).
  const colorIndex = useMemo(() => runColorIndex(runOrder), [runOrder]);

  const context = useMemo(
    () => ({ ns, repo, project, colorIndex, columns, runModels, actions }),
    [ns, repo, project, colorIndex, columns, runModels, actions],
  );

  // The rows in the order the table draws them: group members follow their
  // group header, and a folded group still exports its members — a CSV has no
  // fold, and dropping them would silently export a subset of what the filters
  // selected.
  const exportRows = useMemo(() => groups.flatMap((group) => group.runs), [groups]);
  const exportModels = useMemo(() => {
    const out: Record<string, string[]> = {};
    for (const [run, models] of Object.entries(runModels ?? {})) {
      out[run] = models.map((m) => m.repo.full_name);
    }
    return out;
  }, [runModels]);

  function toggleExpanded(key: string) {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  /** The header cell for one column, from the same list the rows walk. */
  function headerCell(column: RunColumn) {
    const key = runColumnKey(column);
    const sortable = (sortColumn: RunSortColumn, label: string, mono = false) => (
      <SortHeader
        column={sortColumn}
        label={label}
        mono={mono}
        sort={sort}
        onSort={onSort}
        ariaLabel={t("experiments.table.sortByAria", { column: label })}
      />
    );
    switch (column.id) {
      case "select":
        return (
          <Th key={key}>
            <TriStateCheckbox
              checked={allSelected}
              indeterminate={someSelected}
              onChange={onToggleAll}
              aria-label={t("experiments.table.selectAll")}
            />
          </Th>
        );
      case "name":
        return (
          <Th key={key} className="sticky left-0 z-20 bg-bg-sunken">
            {sortable("name", t("experiments.table.colRun"))}
          </Th>
        );
      case "status":
        return <Th key={key}>{sortable("status", t("experiments.table.colStatus"))}</Th>;
      case "tags":
        return <Th key={key}>{t("experiments.table.colTags")}</Th>;
      case "lastStep":
        return <Th key={key}>{sortable("last_step", t("experiments.table.colLastStep"))}</Th>;
      case "metric":
        return <Th key={key}>{sortable(column.sort, column.metric, true)}</Th>;
      case "started":
        return <Th key={key}>{sortable("started_at", t("experiments.table.colStarted"))}</Th>;
      case "models":
        return <Th key={key}>{t("experiments.table.colCheckpoints")}</Th>;
      case "actions":
        return (
          <Th key={key}>
            <span className="sr-only">{t("experiments.table.actionsSr")}</span>
          </Th>
        );
    }
  }

  return (
    <RunTableProvider value={context}>
      <div className="flex flex-col gap-2">
        <Table minWidth={960} className="max-h-[60vh] overflow-y-auto">
          <THead sticky>{columns.map(headerCell)}</THead>
          <TBody>
            {groups.map((group) => {
              if (!group.grouped) {
                const run = group.runs[0];
                if (!run) return null;
                return (
                  <RunRow
                    key={run.name}
                    run={run}
                    selected={selected}
                    onToggle={onToggle}
                    nested={false}
                  />
                );
              }
              return (
                <GroupRows
                  key={`group:${group.key}`}
                  group={group}
                  open={expanded.has(group.key)}
                  onToggleExpanded={() => toggleExpanded(group.key)}
                  selected={selected}
                  onToggle={onToggle}
                  onToggleMany={onToggleMany}
                />
              );
            })}
          </TBody>
        </Table>
        <div className="flex flex-wrap items-center gap-2">
          {hiddenMetrics > 0 && (
            <p className="text-xs font-medium text-fg-subtle">
              {t(
                hiddenMetrics === 1
                  ? "experiments.table.moreMetricsOne"
                  : "experiments.table.moreMetricsOther",
                { count: hiddenMetrics },
              )}
            </p>
          )}
          <div className="ml-auto">
            <CsvDownloadButton
              label={t("experiments.table.exportCsv")}
              filename={csvFilename([ns, repo, project, "runs"])}
              disabled={exportRows.length === 0}
              // Built from the flattened display order, not from the project: what
              // comes out is what is on screen, filters, sort, folded sweeps and
              // metric columns included.
              build={() =>
                runTableCsv(exportRows, metricKeys, {
                  includeModels: hasModels,
                  modelsByRun: exportModels,
                })
              }
            />
          </div>
        </div>
      </div>
    </RunTableProvider>
  );
}
