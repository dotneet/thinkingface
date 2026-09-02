import type { ExpRun } from "@/types/api";

/**
 * Pure helpers behind the grouped, sortable run table.
 *
 * A sweep declares `trackio.init(group="lr-sweep")`, and forty runs that would
 * otherwise fill the table become one foldable row. Everything here is
 * data-in / data-out so the rules — what forms a group, what "best" means for
 * a metric, how a missing value sorts — are testable without a DOM.
 *
 * The ungrouped case is the one that must not change: a run with `group: ""`
 * (every run logged before grouping existed, and every run that simply does
 * not declare one) stays a top-level row in its original position.
 */

/** Metric keys under this prefix are machine telemetry, not results. */
const SYSTEM_PREFIX = "system/";

/** How many metric columns the run table shows before it stops adding them. */
const MAX_METRIC_COLUMNS = 5;

/**
 * One row of the run table: either a single ungrouped run, or a sweep with its
 * members. `key` is "" for the ungrouped kind, which is also what makes the
 * two distinguishable without a discriminant field.
 */
export type RunGroup = {
  /** The declared group name, or "" for a run that declared none. */
  key: string;
  /** Members, in the order they were passed in. */
  runs: ExpRun[];
  /** True when this row stands for a declared group rather than a lone run. */
  grouped: boolean;
};

/**
 * Buckets runs into table rows. Groups keep the position of their first
 * member, so a project that mixes a sweep with a few one-off runs reads in the
 * order the server returned rather than jumping the groups to the top.
 */
export function groupRuns(runs: ExpRun[]): RunGroup[] {
  const out: RunGroup[] = [];
  const byKey = new Map<string, RunGroup>();
  for (const run of runs) {
    const key = run.group ?? "";
    if (!key) {
      out.push({ key: "", runs: [run], grouped: false });
      continue;
    }
    const existing = byKey.get(key);
    if (existing) {
      existing.runs.push(run);
      continue;
    }
    const group: RunGroup = { key, runs: [run], grouped: true };
    byKey.set(key, group);
    out.push(group);
  }
  return out;
}

/** Job types declared inside one group, de-duplicated and sorted. */
export function groupJobTypes(runs: ExpRun[]): string[] {
  const types = new Set<string>();
  for (const run of runs) {
    if (run.job_type) types.add(run.job_type);
  }
  return Array.from(types).sort((a, b) => a.localeCompare(b));
}

/**
 * Which way is "better" for a metric, guessed from its name.
 *
 * There is nothing in the data that says whether 0.2 beats 0.9 — a loss and an
 * accuracy look identical to the server — so the group row's summary uses the
 * same naming convention every training script already follows. A name that
 * matches nothing is treated as "higher is better", which is the common case
 * for the metrics people name after what they measure (f1, bleu, reward).
 */
const LOWER_IS_BETTER =
  /(^|[._/-])(loss|err|error|errors|nll|ppl|perplexity|mae|mse|rmse|wer|cer|fid|regret)([._/-]|$)/i;

export type MetricDirection = "min" | "max";

export function metricDirection(key: string): MetricDirection {
  return LOWER_IS_BETTER.test(key) ? "min" : "max";
}

/**
 * The best value a group reached for one metric, or null when no member
 * recorded it. "Best" follows metricDirection, so a group of losses reports
 * its lowest and a group of accuracies its highest.
 */
export function bestMetric(runs: ExpRun[], key: string): number | null {
  const dir = metricDirection(key);
  let best: number | null = null;
  for (const run of runs) {
    const value = run.summary?.[key];
    if (typeof value !== "number" || !Number.isFinite(value)) continue;
    if (best === null || (dir === "min" ? value < best : value > best)) best = value;
  }
  return best;
}

/** The run that holds a group's best value for a metric, if any does. */
export function bestRunFor(runs: ExpRun[], key: string): ExpRun | undefined {
  const best = bestMetric(runs, key);
  if (best === null) return undefined;
  return runs.find((run) => run.summary?.[key] === best);
}

/**
 * The metric columns the table shows: every non-telemetry metric any run
 * reported a final value for, alphabetical, capped at `limit`.
 *
 * `system/` keys are excluded on purpose — a run logs a dozen of them, they
 * are the same for every run of a sweep, and they would push the metrics that
 * matter off the right edge of the table.
 */
export function metricColumns(runs: ExpRun[], limit = MAX_METRIC_COLUMNS): string[] {
  const keys = new Set<string>();
  for (const run of runs) {
    for (const [key, value] of Object.entries(run.summary ?? {})) {
      if (key.startsWith(SYSTEM_PREFIX)) continue;
      if (typeof value === "number" && Number.isFinite(value)) keys.add(key);
    }
  }
  return Array.from(keys)
    .sort((a, b) => a.localeCompare(b))
    .slice(0, Math.max(0, limit));
}

/** How many metrics were left out of the columns above. */
export function hiddenMetricCount(runs: ExpRun[], limit = MAX_METRIC_COLUMNS): number {
  return Math.max(0, metricColumns(runs, Number.POSITIVE_INFINITY).length - limit);
}

// ------------------------------------------------------------------- sorting

/** A sortable column: a fixed run field, or `metric:<key>`. */
export type RunSortColumn = "name" | "status" | "last_step" | "started_at" | `metric:${string}`;

export type RunSort = { column: RunSortColumn; dir: "asc" | "desc" };

export function metricSortColumn(key: string): RunSortColumn {
  return `metric:${key}`;
}

/** Next state for a header click: first click sorts, the next one flips. */
export function toggleSort(current: RunSort | null, column: RunSortColumn): RunSort {
  if (current?.column === column) {
    return { column, dir: current.dir === "asc" ? "desc" : "asc" };
  }
  // A metric header opens on the useful end (lowest loss / highest accuracy);
  // the rest open ascending, which is alphabetical or oldest-first.
  const metric = column.startsWith("metric:") ? column.slice("metric:".length) : null;
  return { column, dir: metric && metricDirection(metric) === "max" ? "desc" : "asc" };
}

/** The comparable value of one run for a sort column; null means "no value". */
function sortValue(run: ExpRun, column: RunSortColumn): string | number | null {
  const metric = column.startsWith("metric:") ? column.slice("metric:".length) : null;
  if (metric !== null) {
    const value = run.summary?.[metric];
    return typeof value === "number" && Number.isFinite(value) ? value : null;
  }
  switch (column) {
    case "name":
      return run.name;
    case "status":
      return run.status;
    case "started_at":
      return run.started_at ? Date.parse(run.started_at) : null;
    case "last_step":
      return run.last_step;
    default:
      return null;
  }
}

/**
 * Compares two runs for a sort. A run with no value for the column always
 * sorts last, in either direction: "this run never logged an accuracy" is not
 * the same claim as "its accuracy was zero", and floating the blanks to the
 * top of a descending sort would say exactly that.
 */
export function compareRuns(a: ExpRun, b: ExpRun, sort: RunSort): number {
  const av = sortValue(a, sort.column);
  const bv = sortValue(b, sort.column);
  if (av === null && bv === null) return a.name.localeCompare(b.name);
  if (av === null) return 1;
  if (bv === null) return -1;
  const sign = sort.dir === "asc" ? 1 : -1;
  if (typeof av === "string" || typeof bv === "string") {
    return sign * String(av).localeCompare(String(bv));
  }
  if (av === bv) return a.name.localeCompare(b.name);
  return sign * (av < bv ? -1 : 1);
}

/** Sorted copy of `runs`; the input order is kept when `sort` is null. */
export function sortRuns(runs: ExpRun[], sort: RunSort | null): ExpRun[] {
  if (!sort) return runs;
  return [...runs].sort((a, b) => compareRuns(a, b, sort));
}

/**
 * Sorts inside every group and then orders the groups by their own leading
 * run, so "sort by lowest loss" puts the sweep that found the lowest loss on
 * top and, inside it, the run that found it.
 */
export function sortGroups(groups: RunGroup[], sort: RunSort | null): RunGroup[] {
  if (!sort) return groups;
  const sorted = groups.map((g) => ({ ...g, runs: sortRuns(g.runs, sort) }));
  return sorted.sort((a, b) => {
    const [ar] = a.runs;
    const [br] = b.runs;
    if (!ar || !br) return 0;
    return compareRuns(ar, br, sort);
  });
}

// ----------------------------------------------------------------- filtering

export const METRIC_FILTER_OPS = ["<", "<=", ">", ">="] as const;
export type MetricFilterOp = (typeof METRIC_FILTER_OPS)[number];

/** "keep the runs whose `key` is `op` `value`". */
export type MetricFilter = { key: string; op: MetricFilterOp; value: number };

function isMetricFilterOp(raw: string): raw is MetricFilterOp {
  return (METRIC_FILTER_OPS as readonly string[]).includes(raw);
}

/**
 * Builds a filter from the three form controls, or null when it is not usable
 * yet (no metric picked, or the threshold is not a number). A null filter
 * means "no filtering", never "match nothing".
 */
export function buildMetricFilter(key: string, op: string, value: string): MetricFilter | null {
  if (!key || !isMetricFilterOp(op)) return null;
  const parsed = Number(value);
  if (value.trim() === "" || !Number.isFinite(parsed)) return null;
  return { key, op, value: parsed };
}

function matchesMetricFilter(run: ExpRun, filter: MetricFilter): boolean {
  const value = run.summary?.[filter.key];
  // A run that never logged the metric is dropped rather than kept: the
  // filter is a question about the metric, and this run cannot answer it.
  if (typeof value !== "number" || !Number.isFinite(value)) return false;
  switch (filter.op) {
    case "<":
      return value < filter.value;
    case "<=":
      return value <= filter.value;
    case ">":
      return value > filter.value;
    case ">=":
      return value >= filter.value;
  }
}

export function filterByMetric(runs: ExpRun[], filter: MetricFilter | null): ExpRun[] {
  if (!filter) return runs;
  return runs.filter((run) => matchesMetricFilter(run, filter));
}
