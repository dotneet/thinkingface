"use client";

import {
  Archive,
  ArchiveRestore,
  ArrowDown,
  ArrowUp,
  ChevronDown,
  ChevronRight,
  ChevronsUpDown,
  Star,
  Tag,
  Trash2,
} from "lucide-react";
import Link from "next/link";
import { useMemo, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/field";
import { TimeText } from "@/components/ui/time-text";
import { TriStateCheckbox } from "@/components/ui/tri-state-checkbox";
import { colorForRun } from "@/lib/chart-utils";
import { cn } from "@/lib/cn";
import { expRunHref, formatMetricValue } from "@/lib/experiments";
import { formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";
import type { RunModels } from "@/lib/lineage";
import { repoBase } from "@/lib/paths";
import {
  bestMetric,
  bestRunFor,
  groupJobTypes,
  groupRuns,
  hiddenMetricCount,
  metricColumns,
  metricSortColumn,
  type RunGroup,
  type RunSort,
  type RunSortColumn,
  sortGroups,
} from "@/lib/run-grouping";
import type { ExpRun, RunStatus } from "@/types/api";

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
 * How a metric value reads in a cell, or "—" for a run that never logged
 * this metric. Delegates the magnitude-aware formatting to
 * `formatMetricValue` (`lib/experiments.ts`) so the table and the run detail
 * summary cards never disagree on how a value like `2.3e-10` or `1.85e13`
 * should print.
 */
function metricText(value: number | null | undefined): string {
  if (typeof value !== "number" || !Number.isFinite(value)) return "—";
  return formatMetricValue(value);
}

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

  function toggleExpanded(key: string) {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  const sortable = (column: RunSortColumn, label: string, mono = false) => (
    <SortHeader
      column={column}
      label={label}
      mono={mono}
      sort={sort}
      onSort={onSort}
      ariaLabel={t("experiments.table.sortByAria", { column: label })}
    />
  );

  return (
    <div className="flex flex-col gap-2">
      <div className="scroll-x max-h-[60vh] overflow-y-auto rounded-lg border border-border">
        <table className="w-full min-w-[960px] border-collapse text-sm">
          <thead className="sticky top-0 z-10 bg-bg-sunken">
            <tr className="border-b border-border text-left text-xs font-medium text-fg-subtle">
              <th className="px-3 py-2 font-medium">
                <TriStateCheckbox
                  checked={allSelected}
                  indeterminate={someSelected}
                  onChange={onToggleAll}
                  aria-label={t("experiments.table.selectAll")}
                />
              </th>
              <th className="sticky left-0 z-20 bg-bg-sunken px-3 py-2 font-medium">
                {sortable("name", t("experiments.table.colRun"))}
              </th>
              <th className="px-3 py-2 font-medium">
                {sortable("status", t("experiments.table.colStatus"))}
              </th>
              <th className="px-3 py-2 font-medium">{t("experiments.table.colTags")}</th>
              <th className="px-3 py-2 font-medium">
                {sortable("last_step", t("experiments.table.colLastStep"))}
              </th>
              {metricKeys.map((key) => (
                <th key={key} className="px-3 py-2 font-medium">
                  {sortable(metricSortColumn(key), key, true)}
                </th>
              ))}
              <th className="px-3 py-2 font-medium">
                {sortable("started_at", t("experiments.table.colStarted"))}
              </th>
              {hasModels && (
                <th className="px-3 py-2 font-medium">{t("experiments.table.colCheckpoints")}</th>
              )}
              <th className="px-3 py-2 font-medium">
                <span className="sr-only">{t("experiments.table.actionsSr")}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {groups.map((group) => {
              if (!group.grouped) {
                const run = group.runs[0];
                if (!run) return null;
                return (
                  <RunRow
                    key={run.name}
                    run={run}
                    ns={ns}
                    repo={repo}
                    project={project}
                    runOrder={runOrder}
                    metricKeys={metricKeys}
                    selected={selected}
                    onToggle={onToggle}
                    runModels={runModels}
                    hasModels={hasModels}
                    actions={actions}
                    nested={false}
                  />
                );
              }
              const open = expanded.has(group.key);
              return (
                <GroupRows
                  key={`group:${group.key}`}
                  group={group}
                  open={open}
                  onToggleExpanded={() => toggleExpanded(group.key)}
                  ns={ns}
                  repo={repo}
                  project={project}
                  runOrder={runOrder}
                  metricKeys={metricKeys}
                  selected={selected}
                  onToggle={onToggle}
                  onToggleMany={onToggleMany}
                  runModels={runModels}
                  hasModels={hasModels}
                  actions={actions}
                />
              );
            })}
          </tbody>
        </table>
      </div>
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
    </div>
  );
}

/** A group header row followed by its members when it is open. */
function GroupRows({
  group,
  open,
  onToggleExpanded,
  ns,
  repo,
  project,
  runOrder,
  metricKeys,
  selected,
  onToggle,
  onToggleMany,
  runModels,
  hasModels,
  actions,
}: {
  group: RunGroup;
  open: boolean;
  onToggleExpanded: () => void;
  ns: string;
  repo: string;
  project: string;
  runOrder: string[];
  metricKeys: string[];
  selected: Set<string>;
  onToggle: (name: string) => void;
  onToggleMany: (names: string[], select: boolean) => void;
  runModels?: RunModels;
  hasModels: boolean;
  actions: RunTableActions;
}) {
  const t = useT();
  const names = group.runs.map((r) => r.name);
  const allSelected = names.every((name) => selected.has(name));
  const someSelected = !allSelected && names.some((name) => selected.has(name));
  const jobTypes = groupJobTypes(group.runs);
  const lastStep = Math.max(...group.runs.map((r) => r.last_step));
  const started = group.runs
    .map((r) => r.started_at)
    .filter((value): value is string => Boolean(value))
    .sort()[0];

  const statusCounts = new Map<RunStatus, number>();
  for (const run of group.runs) {
    statusCounts.set(run.status, (statusCounts.get(run.status) ?? 0) + 1);
  }

  return (
    <>
      <tr className="border-b border-border bg-bg-sunken">
        <td className="px-3 py-2">
          <TriStateCheckbox
            checked={allSelected}
            indeterminate={someSelected}
            onChange={() => onToggleMany(names, !allSelected)}
            aria-label={t("experiments.table.selectGroup", { name: group.key })}
          />
        </td>
        <td className="sticky left-0 z-[1] bg-bg-sunken px-3 py-2">
          <span className="flex items-center gap-2 font-medium">
            <Button
              size="sm"
              variant="ghost"
              onClick={onToggleExpanded}
              aria-expanded={open}
              aria-label={
                open
                  ? t("experiments.table.collapseGroupAria", { name: group.key })
                  : t("experiments.table.expandGroupAria", { name: group.key })
              }
            >
              {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
            </Button>
            <span>{group.key}</span>
            <Badge>
              {t(
                group.runs.length === 1
                  ? "experiments.table.groupMembersOne"
                  : "experiments.table.groupMembersOther",
                { count: group.runs.length },
              )}
            </Badge>
            {jobTypes.map((type) => (
              <Badge key={type} tone="accent">
                {type}
              </Badge>
            ))}
          </span>
        </td>
        <td className="px-3 py-2">
          <div className="flex flex-wrap gap-1.5">
            {Array.from(statusCounts.entries()).map(([status, count]) => (
              <Badge key={status} tone={statusTone(status)}>
                {t("experiments.table.statusCount", { status: statusLabel(t, status), count })}
              </Badge>
            ))}
          </div>
        </td>
        <td className="px-3 py-2 text-fg-subtle">—</td>
        <td className="px-3 py-2 tabular-nums">{formatNumber(lastStep)}</td>
        {metricKeys.map((key) => {
          const best = bestMetric(group.runs, key);
          const holder = bestRunFor(group.runs, key);
          return (
            <td key={key} className="px-3 py-2 tabular-nums">
              {best === null ? (
                <span className="text-fg-subtle">—</span>
              ) : (
                <span
                  className="text-fg"
                  title={t("experiments.table.bestInGroup", {
                    metric: key,
                    run: holder?.name ?? "",
                  })}
                >
                  {metricText(best)}
                </span>
              )}
            </td>
          );
        })}
        <td className="px-3 py-2 text-fg-muted">
          <TimeText iso={started ?? null} style="dateTime" />
        </td>
        {hasModels && <td className="px-3 py-2 text-fg-subtle">—</td>}
        <td className="px-3 py-2" />
      </tr>
      {open &&
        group.runs.map((run) => (
          <RunRow
            key={run.name}
            run={run}
            ns={ns}
            repo={repo}
            project={project}
            runOrder={runOrder}
            metricKeys={metricKeys}
            selected={selected}
            onToggle={onToggle}
            runModels={runModels}
            hasModels={hasModels}
            actions={actions}
            nested
          />
        ))}
    </>
  );
}

function statusTone(status: RunStatus): "positive" | "negative" | "accent" {
  return status === "finished" ? "positive" : status === "failed" ? "negative" : "accent";
}

function statusLabel(t: ReturnType<typeof useT>, status: RunStatus): string {
  return status === "finished"
    ? t("experiments.table.statusFinished")
    : status === "failed"
      ? t("experiments.table.statusFailed")
      : t("experiments.table.statusRunning");
}

function RunRow({
  run,
  ns,
  repo,
  project,
  runOrder,
  metricKeys,
  selected,
  onToggle,
  runModels,
  hasModels,
  actions,
  nested,
}: {
  run: ExpRun;
  ns: string;
  repo: string;
  project: string;
  runOrder: string[];
  metricKeys: string[];
  selected: Set<string>;
  onToggle: (name: string) => void;
  runModels?: RunModels;
  hasModels: boolean;
  actions: RunTableActions;
  /** True for a member of an expanded group, which is indented. */
  nested: boolean;
}) {
  const t = useT();
  const busy = actions.pendingRun === run.name;
  const models = runModels?.[run.name] ?? [];

  return (
    <tr
      className={cn(
        "group border-b border-border last:border-0 hover:bg-bg-hover",
        run.archived && "opacity-60",
      )}
    >
      <td className="px-3 py-2">
        <Checkbox
          checked={selected.has(run.name)}
          onChange={() => onToggle(run.name)}
          aria-label={t("experiments.table.selectRun", { name: run.name })}
        />
      </td>
      <td className="sticky left-0 z-[1] bg-bg px-3 py-2 group-hover:bg-bg-hover">
        <span
          className={cn(
            "flex items-center gap-2 font-medium",
            nested && "pl-6",
            run.archived && "text-fg-subtle line-through",
          )}
        >
          <span
            className="h-2.5 w-2.5 shrink-0 rounded-full"
            style={{ background: colorForRun(runOrder.indexOf(run.name)) }}
          />
          <Link
            href={expRunHref(ns, repo, project, run.name)}
            className="hover:text-accent hover:underline"
          >
            {run.name}
          </Link>
          {run.is_baseline && <Badge tone="accent">{t("experiments.table.baselineBadge")}</Badge>}
          {run.archived && <Badge>{t("experiments.table.archivedBadge")}</Badge>}
        </span>
      </td>
      <td className="px-3 py-2">
        <Badge tone={statusTone(run.status)}>{statusLabel(t, run.status)}</Badge>
      </td>
      <td className="px-3 py-2">
        {run.tags.length === 0 ? (
          <span className="text-fg-subtle">—</span>
        ) : (
          <div className="flex flex-wrap gap-1.5">
            {run.tags.map((tag) => (
              <Badge key={tag}>{tag}</Badge>
            ))}
          </div>
        )}
      </td>
      <td className="px-3 py-2 tabular-nums">{formatNumber(run.last_step)}</td>
      {metricKeys.map((key) => {
        const value = run.summary?.[key];
        return (
          <td key={key} className="px-3 py-2 tabular-nums">
            {typeof value === "number" ? (
              metricText(value)
            ) : (
              <span className="text-fg-subtle">—</span>
            )}
          </td>
        );
      })}
      <td className="px-3 py-2 text-fg-muted">
        <TimeText iso={run.started_at} style="dateTime" />
      </td>
      {hasModels && (
        <td className="px-3 py-2">
          {models.length === 0 ? (
            <span className="text-fg-subtle">—</span>
          ) : (
            <div className="flex flex-col gap-0.5">
              {models.map((m) => (
                <Link
                  key={m.repo.full_name}
                  href={repoBase(m.repo.kind, m.repo.namespace, m.repo.name)}
                  className="font-mono text-xs text-accent hover:underline"
                >
                  {m.repo.full_name}
                </Link>
              ))}
            </div>
          )}
        </td>
      )}
      <td className="px-3 py-2">
        <div className="flex items-center justify-end gap-1">
          <Button
            size="sm"
            variant="ghost"
            disabled={busy}
            onClick={() => actions.onToggleBaseline(run)}
            aria-pressed={run.is_baseline}
            aria-label={
              run.is_baseline
                ? t("experiments.table.clearBaselineAria", { name: run.name })
                : t("experiments.table.setBaselineAria", { name: run.name })
            }
            title={
              run.is_baseline
                ? t("experiments.table.clearBaseline")
                : t("experiments.table.setBaseline")
            }
          >
            <Star size={14} className={run.is_baseline ? "text-accent" : undefined} />
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={busy}
            onClick={() => actions.onEditTags(run)}
            aria-label={t("experiments.table.editTagsAria", { name: run.name })}
            title={t("experiments.table.editTags")}
          >
            <Tag size={14} />
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={busy}
            onClick={() => actions.onToggleArchived(run)}
            aria-pressed={run.archived}
            aria-label={
              run.archived
                ? t("experiments.table.unarchiveAria", { name: run.name })
                : t("experiments.table.archiveAria", { name: run.name })
            }
            title={run.archived ? t("experiments.table.unarchive") : t("experiments.table.archive")}
          >
            {run.archived ? <ArchiveRestore size={14} /> : <Archive size={14} />}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={busy}
            onClick={() => actions.onDelete(run)}
            aria-label={t("experiments.deleteRun.rowAria", { name: run.name })}
            title={t("experiments.deleteRun.button")}
          >
            <Trash2 size={14} className="text-negative" />
          </Button>
        </div>
      </td>
    </tr>
  );
}

/** A column header that sorts the table, showing the current direction. */
function SortHeader({
  column,
  label,
  mono,
  sort,
  onSort,
  ariaLabel,
}: {
  column: RunSortColumn;
  label: string;
  mono: boolean;
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
