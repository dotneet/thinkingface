"use client";

import {
  Archive,
  ArchiveRestore,
  ChevronDown,
  ChevronRight,
  EllipsisVertical,
  ExternalLink,
  FlaskConical,
  ListFilter,
  Star,
  Tag,
  Trash2,
  Trophy,
} from "lucide-react";
import Link from "next/link";
import { useEffect, useMemo, useState } from "react";

import { RunFilterBar } from "@/components/experiments/run-filter-bar";
import { RunStatusIcon } from "@/components/experiments/run-status-indicator";
import type { RunTableActions } from "@/components/experiments/run-table-context";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import { EmptyState } from "@/components/ui/empty-state";
import { Checkbox, Select } from "@/components/ui/field";
import { FilterInput } from "@/components/ui/search-input";
import { SpinnerSlot } from "@/components/ui/spinner";
import { TimeText } from "@/components/ui/time-text";
import { TriStateCheckbox } from "@/components/ui/tri-state-checkbox";
import type { RunFilters } from "@/hooks/use-run-filters";
import { colorForRun } from "@/lib/chart-utils";
import { cn } from "@/lib/cn";
import { formatColumnValue } from "@/lib/exp-chart-ticks";
import { type BestRuns, type MetricGoals, metricGoal } from "@/lib/exp-goals";
import {
  bestOf,
  bestValue,
  type SidebarSort,
  STATUS_FILTERS,
  type StatusFilter,
  sidebarGroups,
  sidebarSortValue,
  parseSidebarSort,
} from "@/lib/exp-workspace";
import { expRunHref } from "@/lib/experiments";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import type { RunGroup } from "@/lib/run-grouping";
import type { ExpRun } from "@/types/api";

const STATUS_LABEL: Record<StatusFilter, MessageKey> = {
  all: "experiments.sidebar.statusAll",
  running: "experiments.sidebar.statusRunning",
  stale: "experiments.sidebar.statusStale",
  finished: "experiments.sidebar.statusFinished",
  failed: "experiments.sidebar.statusFailed",
};

/** Fixed row pitch: every run row and group header is exactly this tall. */
const ROW = "h-8";

/** How many runs "Top N" selects. */
export const TOP_N = 5;

export type RunSidebarProps = {
  ns: string;
  repo: string;
  project: string;
  /** The runs the list shows: every filter below already applied. */
  listedRuns: ExpRun[];
  statusCounts: Record<StatusFilter, number>;
  query: string;
  onQueryChange: (query: string) => void;
  status: StatusFilter;
  onStatusChange: (status: StatusFilter) => void;
  sort: SidebarSort;
  onSortChange: (sort: SidebarSort) => void;
  /** Goal metrics, in workspace order; offered as "Best …" sorts. */
  goalMetrics: string[];
  /** Every other (non-system) metric, offered under "Other metrics". */
  otherMetrics: string[];
  /** The metric the value column shows, or undefined for "updated" times. */
  valueMetric: string | undefined;
  showArchived: boolean;
  onShowArchivedChange: (show: boolean) => void;
  archivedCount: number;
  filters: RunFilters;
  onFiltersChange: (patch: Partial<RunFilters>) => void;
  /** Number of tag / metric filters in effect (for the disclosure's badge). */
  activeExtraFilters: number;
  tags: string[];
  metricKeys: string[];
  onClearFilters: () => void;
  selected: ReadonlySet<string>;
  onToggle: (name: string) => void;
  onToggleMany: (names: string[], select: boolean) => void;
  /** Replaces the selection. */
  onSelectOnly: (names: string[]) => void;
  /** Runs plotted in the workspace. */
  selectedCount: number;
  /** Of those, how many the current filters hide from this list. */
  hiddenSelectedCount: number;
  runningNames: string[];
  topNames: string[];
  colorIndex: ReadonlyMap<string, number>;
  goals: MetricGoals;
  best: BestRuns;
  canWrite: boolean;
  actions: RunTableActions;
  /** An annotation write is in flight. */
  saving: boolean;
  /** "pane": the ≥lg sticky column. "sheet": inside the small-screen dialog. */
  variant: "pane" | "sheet";
};

function collapsedKey(ns: string, repo: string, project: string): string {
  return `tf-exp-collapsed:${ns}/${repo}/${project}`;
}

/**
 * The run list that drives the workspace: always on screen next to what it
 * selects, so looking at another run never means scrolling to the bottom of
 * the page.
 *
 * Every row is one fixed-height line — a checkbox in the run's chart colour, the
 * name (a link to the run page, truncated with the full name on hover), a
 * status glyph and the value the list is sorted by — so the list reads as a
 * list however long the names are. Sweeps fold into group headers with their
 * own tri-state box; the fold state is remembered per project.
 */
export function RunSidebar(props: RunSidebarProps) {
  const {
    ns,
    repo,
    project,
    listedRuns,
    statusCounts,
    query,
    onQueryChange,
    status,
    onStatusChange,
    sort,
    onSortChange,
    goalMetrics,
    otherMetrics,
    valueMetric,
    showArchived,
    onShowArchivedChange,
    archivedCount,
    filters,
    onFiltersChange,
    activeExtraFilters,
    tags,
    metricKeys,
    onClearFilters,
    selected,
    onToggleMany,
    onSelectOnly,
    selectedCount,
    hiddenSelectedCount,
    runningNames,
    topNames,
    saving,
    variant,
  } = props;
  const t = useT();
  const [moreOpen, setMoreOpen] = useState(activeExtraFilters > 0);
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set());

  // Read after mount: the server render cannot see localStorage, and reading
  // it during the first render would make the two disagree.
  useEffect(() => {
    try {
      const raw = window.localStorage.getItem(collapsedKey(ns, repo, project));
      const parsed: unknown = raw ? JSON.parse(raw) : [];
      if (Array.isArray(parsed)) {
        setCollapsed(new Set(parsed.filter((v): v is string => typeof v === "string")));
      }
    } catch {
      // Storage blocked or garbage: every group simply starts open.
    }
  }, [ns, repo, project]);

  function toggleCollapsed(key: string) {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      try {
        window.localStorage.setItem(collapsedKey(ns, repo, project), JSON.stringify([...next]));
      } catch {
        // Remembering the fold is a convenience; losing it is fine.
      }
      return next;
    });
  }

  const groups = useMemo(
    () => sidebarGroups(listedRuns, sort, props.goals),
    [listedRuns, sort, props.goals],
  );
  // A search shows its matches, whatever was folded before.
  const searching = query.trim() !== "" || status !== "all" || activeExtraFilters > 0;

  return (
    <div className={cn("flex min-h-0 flex-col", variant === "pane" ? "h-full" : "h-[62dvh]")}>
      <div className="flex shrink-0 flex-col gap-2 border-b border-border p-3">
        <FilterInput
          value={query}
          onChange={onQueryChange}
          placeholder={t("experiments.sidebar.searchPlaceholder")}
          label={t("experiments.sidebar.searchAria")}
        />

        <fieldset
          aria-label={t("experiments.sidebar.statusAria")}
          className="flex min-w-0 flex-wrap gap-1"
        >
          {STATUS_FILTERS.map((value) => {
            const active = value === status;
            const label = t(STATUS_LABEL[value]);
            return (
              <Button
                key={value}
                size="sm"
                variant="ghost"
                aria-pressed={active}
                aria-label={t("experiments.sidebar.statusCountAria", {
                  status: label,
                  count: statusCounts[value],
                })}
                onClick={() => onStatusChange(active && value !== "all" ? "all" : value)}
                className={cn(
                  "gap-1.5 whitespace-nowrap border-border px-1.5 py-0.5",
                  active &&
                    "border-transparent bg-accent-muted text-accent-strong hover:bg-accent-muted",
                )}
              >
                {value !== "all" && value !== "finished" && <RunStatusIcon status={value} />}
                {label}
                <span className="tabular-nums opacity-80">{statusCounts[value]}</span>
              </Button>
            );
          })}
        </fieldset>

        <div className="flex items-center gap-2">
          <label className="flex min-w-0 flex-1 items-center gap-2">
            <span className="shrink-0 text-xs font-medium text-fg-subtle">
              {t("experiments.sidebar.sortLabel")}
            </span>
            <Select
              value={sidebarSortValue(sort)}
              onChange={(e) => onSortChange(parseSidebarSort(e.target.value))}
              className="min-w-0 flex-1 bg-bg-raised px-2 py-1 text-xs"
            >
              {goalMetrics.length > 0 && (
                <optgroup label={t("experiments.sidebar.sortGoalMetrics")}>
                  {goalMetrics.map((metric) => (
                    <option key={metric} value={sidebarSortValue({ kind: "metric", metric })}>
                      {t("experiments.sidebar.sortBest", { metric })}
                    </option>
                  ))}
                </optgroup>
              )}
              <option value="updated">{t("experiments.sidebar.sortUpdated")}</option>
              <option value="name">{t("experiments.sidebar.sortName")}</option>
              {otherMetrics.length > 0 && (
                <optgroup label={t("experiments.sidebar.sortOtherMetrics")}>
                  {otherMetrics.map((metric) => (
                    <option key={metric} value={sidebarSortValue({ kind: "metric", metric })}>
                      {metric}
                    </option>
                  ))}
                </optgroup>
              )}
            </Select>
          </label>
          <Button
            size="sm"
            variant="ghost"
            aria-expanded={moreOpen}
            onClick={() => setMoreOpen((v) => !v)}
            title={t("experiments.sidebar.moreFilters")}
            aria-label={t("experiments.sidebar.moreFilters")}
            className={cn("relative", activeExtraFilters > 0 && "text-accent-strong")}
          >
            <ListFilter size={14} />
            {activeExtraFilters > 0 && <span className="tabular-nums">{activeExtraFilters}</span>}
          </Button>
        </div>

        {moreOpen && (
          <div className="rounded-md border border-border bg-bg-sunken p-2">
            <RunFilterBar
              filters={filters}
              onChange={onFiltersChange}
              tags={tags}
              metricKeys={metricKeys}
            />
          </div>
        )}

        <div className="flex items-center gap-1">
          <span className="mr-auto min-w-0 truncate text-xs font-medium text-fg-muted">
            {t("experiments.sidebar.selectedCount", { count: selectedCount })}
            {hiddenSelectedCount > 0 && (
              <span className="text-fg-subtle">
                {" · "}
                {t("experiments.sidebar.hiddenSelected", { count: hiddenSelectedCount })}
              </span>
            )}
          </span>
          <fieldset
            aria-label={t("experiments.sidebar.quickSelectAria")}
            className="flex shrink-0 gap-0.5"
          >
            <Button
              size="sm"
              variant="ghost"
              disabled={runningNames.length === 0}
              onClick={() => onSelectOnly(runningNames)}
              title={t("experiments.sidebar.selectRunningTitle")}
              className="px-1.5"
            >
              {t("experiments.sidebar.selectRunning")}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={topNames.length === 0}
              onClick={() => onSelectOnly(topNames)}
              title={
                valueMetric
                  ? t("experiments.sidebar.selectTopTitle", { metric: valueMetric })
                  : t("experiments.sidebar.selectTopNoGoal")
              }
              className="px-1.5"
            >
              {t("experiments.sidebar.selectTop")}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={selectedCount === 0}
              onClick={() => onSelectOnly([])}
              title={t("experiments.sidebar.clearTitle")}
              className="px-1.5"
            >
              {t("experiments.sidebar.clear")}
            </Button>
          </fieldset>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-1.5 py-1.5">
        {listedRuns.length === 0 ? (
          <EmptyState
            icon={FlaskConical}
            title={t("experiments.sidebar.noMatchTitle")}
            description={t("experiments.sidebar.noMatchDescription")}
            action={
              <Button size="sm" variant="secondary" onClick={onClearFilters}>
                {t("experiments.sidebar.clearFilters")}
              </Button>
            }
          />
        ) : (
          <>
            {/* What the number at each row's right edge is: the best value in
              the goal's direction, which differs from the Table view's
              "last" column -- without this caption the two read as a
              contradiction. */}
            <p
              aria-hidden
              className="flex items-center justify-between gap-2 px-2 pb-1 text-[11px] font-medium text-fg-subtle"
            >
              <span>{t("experiments.sidebar.columnRun")}</span>
              <span className="truncate" title={valueMetric}>
                {valueMetric
                  ? t("experiments.sidebar.columnBest", { metric: valueMetric })
                  : t("experiments.sidebar.columnUpdated")}
              </span>
            </p>
            <ul aria-label={t("experiments.sidebar.listAria")} className="flex flex-col">
              {groups.map((group) =>
                group.grouped ? (
                  <SidebarGroup
                    key={`group:${group.key}`}
                    group={group}
                    open={searching || !collapsed.has(group.key)}
                    onToggleOpen={() => toggleCollapsed(group.key)}
                    selected={selected}
                    onToggleMany={onToggleMany}
                    sidebar={props}
                  />
                ) : group.runs[0] ? (
                  <SidebarRunRow
                    key={group.runs[0].name}
                    run={group.runs[0]}
                    nested={false}
                    sidebar={props}
                  />
                ) : null,
              )}
            </ul>
          </>
        )}
      </div>

      <div className="flex shrink-0 items-center gap-2 border-t border-border px-3 py-2">
        <label className="flex items-center gap-2 text-xs font-medium text-fg-subtle">
          <Checkbox
            checked={showArchived}
            onChange={(e) => onShowArchivedChange(e.target.checked)}
          />
          {t("experiments.sidebar.showArchived", { count: archivedCount })}
        </label>
        <SpinnerSlot
          active={saving}
          size={14}
          label={t("experiments.dashboard.savingAnnotation")}
          className="ml-auto"
        />
      </div>
    </div>
  );
}

/** The value a row shows at its right edge. */
function RowValue({
  run,
  metric,
  goals,
  best,
}: {
  run: ExpRun;
  metric: string | undefined;
  goals: MetricGoals;
  best: BestRuns;
}) {
  const t = useT();
  if (!metric) {
    return (
      <TimeText
        iso={run.updated_at}
        style="relative"
        className="truncate text-xs font-medium text-fg-subtle"
      />
    );
  }
  const value = bestValue(run, metric, goals);
  const isBest = best[metric] === run.name;
  return (
    <>
      <span
        className={cn(
          "text-xs tabular-nums",
          isBest ? "font-semibold text-fg" : "font-medium text-fg-muted",
        )}
        title={value === undefined ? undefined : `${metric}: ${value}`}
      >
        {value === undefined ? t("experiments.sidebar.valueNone") : formatColumnValue(value)}
      </span>
      <span className="inline-flex w-3 shrink-0 justify-center">
        {isBest && (
          <Trophy
            size={12}
            className="text-warning"
            role="img"
            aria-label={
              metricGoal(goals, metric) === "max"
                ? t("experiments.goals.bestMax", { metric })
                : t("experiments.goals.bestMin", { metric })
            }
          />
        )}
      </span>
    </>
  );
}

function SidebarRunRow({
  run,
  nested,
  sidebar,
}: {
  run: ExpRun;
  nested: boolean;
  sidebar: RunSidebarProps;
}) {
  const t = useT();
  const { ns, repo, project, selected, onToggle, colorIndex, valueMetric, goals, best } = sidebar;
  const checked = selected.has(run.name);
  const color = colorForRun(colorIndex.get(run.name) ?? -1);
  const href = expRunHref(ns, repo, project, run.name);

  return (
    <li
      className={cn(
        "group/row flex items-center gap-1 rounded-md pr-0.5 hover:bg-bg-hover",
        ROW,
        run.archived && "opacity-60",
      )}
    >
      {/* The label is the row: a click anywhere on it that is not the name
          link toggles the checkbox, natively and with the keyboard too. */}
      <label
        className={cn(
          "flex h-full min-w-0 flex-1 cursor-pointer items-center gap-2 pl-2",
          nested && "pl-6",
        )}
      >
        <Checkbox
          checked={checked}
          onChange={() => onToggle(run.name)}
          aria-label={t("experiments.sidebar.toggleRunAria", { name: run.name })}
          style={{ accentColor: color }}
          className="h-3.5 w-3.5 cursor-pointer"
        />
        <span className="flex min-w-0 flex-1 items-center gap-1.5">
          <Link
            href={href}
            title={run.name}
            className={cn(
              "min-w-0 truncate text-sm hover:text-accent hover:underline",
              checked ? "font-medium text-fg" : "text-fg-muted",
              run.archived && "line-through",
            )}
          >
            {run.name}
          </Link>
          {run.is_baseline && (
            <Star
              size={11}
              className="shrink-0 text-accent"
              role="img"
              aria-label={t("experiments.table.baselineBadge")}
            />
          )}
        </span>
        <RunStatusIcon status={run.status} />
        <span className="flex w-[4.5rem] shrink-0 items-center justify-end gap-1">
          <RowValue run={run} metric={valueMetric} goals={goals} best={best} />
        </span>
      </label>
      {sidebar.canWrite && <RunRowMenu run={run} href={href} sidebar={sidebar} />}
    </li>
  );
}

/**
 * The per-run actions the old table rows carried as a button cluster — open,
 * baseline, tags, archive, delete — behind one kebab so the row stays one
 * line. The trigger shows on hover or focus (always on a touch screen, where
 * there is no hover).
 */
function RunRowMenu({
  run,
  href,
  sidebar,
}: {
  run: ExpRun;
  href: string;
  sidebar: RunSidebarProps;
}) {
  const t = useT();
  const { actions } = sidebar;
  const busy = actions.pendingRun === run.name;
  return (
    <DropdownMenu
      align="end"
      trigger={({ open, toggle, triggerProps }) => (
        <Button
          size="sm"
          variant="ghost"
          onClick={toggle}
          {...triggerProps}
          aria-label={t("experiments.sidebar.runMenuAria", { name: run.name })}
          className={cn(
            "h-6 w-6 px-0 py-0 opacity-0 focus-visible:opacity-100 group-hover/row:opacity-100 pointer-coarse:opacity-100",
            (open || sidebar.variant === "sheet") && "opacity-100",
          )}
        >
          <EllipsisVertical size={14} />
        </Button>
      )}
    >
      {({ close }) => (
        <>
          <Link
            href={href}
            className="flex w-full items-center gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <ExternalLink size={14} />
            {t("experiments.sidebar.openRun")}
          </Link>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            disabled={busy}
            onClick={() => {
              close();
              actions.onToggleBaseline(run);
            }}
          >
            <Star size={14} />
            {run.is_baseline
              ? t("experiments.table.clearBaseline")
              : t("experiments.table.setBaseline")}
          </DropdownMenuItem>
          <DropdownMenuItem
            disabled={busy}
            onClick={() => {
              close();
              actions.onEditTags(run);
            }}
          >
            <Tag size={14} />
            {t("experiments.table.editTags")}
          </DropdownMenuItem>
          <DropdownMenuItem
            disabled={busy}
            onClick={() => {
              close();
              actions.onToggleArchived(run);
            }}
          >
            {run.archived ? <ArchiveRestore size={14} /> : <Archive size={14} />}
            {run.archived ? t("experiments.table.unarchive") : t("experiments.table.archive")}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            disabled={busy}
            onClick={() => {
              close();
              actions.onDelete(run);
            }}
            className="text-negative hover:text-negative-strong"
          >
            <Trash2 size={14} />
            {t("experiments.deleteRun.button")}
          </DropdownMenuItem>
        </>
      )}
    </DropdownMenu>
  );
}

function SidebarGroup({
  group,
  open,
  onToggleOpen,
  selected,
  onToggleMany,
  sidebar,
}: {
  group: RunGroup;
  open: boolean;
  onToggleOpen: () => void;
  selected: ReadonlySet<string>;
  onToggleMany: (names: string[], select: boolean) => void;
  sidebar: RunSidebarProps;
}) {
  const t = useT();
  const { valueMetric, goals, best } = sidebar;
  const names = group.runs.map((r) => r.name);
  const allSelected = names.every((name) => selected.has(name));
  const someSelected = !allSelected && names.some((name) => selected.has(name));
  const groupBest = valueMetric ? bestOf(group.runs, valueMetric, goals) : null;
  const holdsBest = valueMetric ? group.runs.some((r) => best[valueMetric] === r.name) : false;

  return (
    <li>
      <div className={cn("flex items-center gap-1 rounded-md pl-2 pr-0.5 hover:bg-bg-hover", ROW)}>
        <TriStateCheckbox
          checked={allSelected}
          indeterminate={someSelected}
          onChange={() => onToggleMany(names, !allSelected)}
          aria-label={t("experiments.table.selectGroup", { name: group.key })}
          className="mr-1 cursor-pointer"
        />
        <Button
          size="sm"
          variant="ghost"
          onClick={onToggleOpen}
          aria-expanded={open}
          aria-label={
            open
              ? t("experiments.table.collapseGroupAria", { name: group.key })
              : t("experiments.table.expandGroupAria", { name: group.key })
          }
          className="h-7 min-w-0 flex-1 justify-start gap-1 px-1 text-sm font-semibold text-fg"
        >
          {open ? (
            <ChevronDown size={14} className="shrink-0" />
          ) : (
            <ChevronRight size={14} className="shrink-0" />
          )}
          <span className="min-w-0 truncate" title={group.key}>
            {group.key}
          </span>
          <span className="shrink-0 text-xs font-medium tabular-nums text-fg-subtle">
            {group.runs.length}
          </span>
        </Button>
        <span
          className="flex w-[4.5rem] shrink-0 items-center justify-end gap-1"
          title={
            groupBest && valueMetric
              ? t("experiments.sidebar.bestInGroup", {
                  metric: valueMetric,
                  name: group.key,
                  run: groupBest.run,
                })
              : undefined
          }
        >
          {groupBest && (
            <>
              <span className="text-xs font-medium tabular-nums text-fg-muted">
                {formatColumnValue(groupBest.value)}
              </span>
              <span className="inline-flex w-3 shrink-0 justify-center">
                {holdsBest && <Trophy size={12} className="text-warning" aria-hidden />}
              </span>
            </>
          )}
        </span>
        {/* Keeps the value column aligned with the run rows' kebab slot. */}
        {sidebar.canWrite && <span className="w-6 shrink-0" aria-hidden />}
      </div>
      {open && (
        <ul className="flex flex-col">
          {group.runs.map((run) => (
            <SidebarRunRow key={run.name} run={run} nested sidebar={sidebar} />
          ))}
        </ul>
      )}
    </li>
  );
}
