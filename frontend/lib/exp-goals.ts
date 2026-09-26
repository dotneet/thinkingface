import { metricDirection, type RunSort, type RunSortColumn, toggleSort } from "@/lib/run-grouping";
import type { ExpRun, ExpRunListResponse, MetricGoal } from "@/types/api";

/**
 * Pure rules behind metric goals, the best-run marker and the run table's
 * last / min / max / best value switch (docs/dev/agent-features.md §2.1–2.3).
 *
 * Everything is data-in / data-out so it is testable without a DOM; the
 * components only decide where the results go.
 */

/** Which of a run's per-metric summaries the table shows. */
export type SummaryMode = "last" | "min" | "max" | "best";

export const SUMMARY_MODES: readonly SummaryMode[] = ["last", "min", "max", "best"];

/** Metric → declared goal. A metric with no goal is absent. */
export type MetricGoals = Readonly<Record<string, MetricGoal>>;

/** Metric → name of the best non-archived run, as the run listing reports it. */
export type BestRuns = Readonly<Record<string, string>>;

/**
 * The goals a run listing carries. A server that predates goals omits the
 * field, which must read as "no goals", never as a crash.
 */
export function runListGoals(list: Partial<ExpRunListResponse> | undefined): MetricGoals {
  return list?.metric_goals ?? {};
}

/** The best-run map a run listing carries; `{}` from a server without goals. */
export function runListBest(list: Partial<ExpRunListResponse> | undefined): BestRuns {
  return list?.best ?? {};
}

/** The declared goal of one metric, or undefined when there is none. */
export function metricGoal(goals: MetricGoals, key: string): MetricGoal | undefined {
  const goal = goals[key];
  return goal === "min" || goal === "max" ? goal : undefined;
}

export function hasAnyGoal(goals: MetricGoals): boolean {
  return Object.keys(goals).some((key) => metricGoal(goals, key) !== undefined);
}

/**
 * Which way is better for a metric: the declared goal when there is one, the
 * naming heuristic (`metricDirection`) otherwise. A declaration always beats
 * the guess — `score_error_rate_inverse` is exactly the kind of name the
 * heuristic gets wrong and a goal exists to correct.
 */
export function effectiveDirection(goals: MetricGoals, key: string): MetricGoal {
  return metricGoal(goals, key) ?? metricDirection(key);
}

function finite(value: number | undefined): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

/**
 * One run's value for one metric under a summary mode, or undefined when the
 * run has none. "best" is the minimum for a metric whose goal (or, lacking
 * one, name) says lower is better and the maximum otherwise.
 *
 * `summary_min` / `summary_max` are optional-chained: a server that predates
 * them sends neither, and "no min recorded" is a dash, not a crash.
 */
export function summaryValue(
  run: ExpRun,
  key: string,
  mode: SummaryMode,
  goals: MetricGoals,
): number | undefined {
  switch (mode) {
    case "last":
      return finite(run.summary?.[key]);
    case "min":
      return finite(run.summary_min?.[key]);
    case "max":
      return finite(run.summary_max?.[key]);
    case "best":
      return effectiveDirection(goals, key) === "min"
        ? finite(run.summary_min?.[key])
        : finite(run.summary_max?.[key]);
  }
}

/**
 * The runs as the table should read them under `mode`: each run's `summary`
 * replaced by the chosen summary, everything else untouched.
 *
 * Projecting the runs once, above the table, is what keeps every consumer of
 * `summary` — the cells, the group rows' best value, the sort, the metric
 * filter, the scatter and parallel plots — reading the same numbers, rather
 * than each of them growing its own mode parameter. "last" returns the input
 * array itself, so the default mode costs nothing and keeps memo identities.
 *
 * Only a display view: never send a projected run back to the server, and
 * build exports from the original runs (the CSV carries all three summaries).
 */
export function projectRunSummaries(
  runs: ExpRun[],
  mode: SummaryMode,
  goals: MetricGoals,
): ExpRun[] {
  if (mode === "last") return runs;
  return runs.map((run) => {
    const keys = new Set([
      ...Object.keys(run.summary ?? {}),
      ...Object.keys(run.summary_min ?? {}),
      ...Object.keys(run.summary_max ?? {}),
    ]);
    const summary: Record<string, number> = {};
    for (const key of keys) {
      const value = summaryValue(run, key, mode, goals);
      if (value !== undefined) summary[key] = value;
    }
    return { ...run, summary };
  });
}

/**
 * True when `runName` is the server's best run for `key`. The server decides
 * (lowest `summary_min` / highest `summary_max` among non-archived runs), so
 * the badge never disagrees with `tf experiments runs --sort best:<metric>`.
 */
export function isBestRun(best: BestRuns, key: string, runName: string): boolean {
  return best[key] === runName;
}

/**
 * The best value a set of runs reached for one metric, and who reached it,
 * reading each run's `summary` (already projected to the table's mode) in the
 * metric's effective direction. What a folded sweep's row shows.
 */
export function bestInRuns(
  runs: readonly ExpRun[],
  key: string,
  goals: MetricGoals,
): { value: number; run: string } | null {
  const dir = effectiveDirection(goals, key);
  let out: { value: number; run: string } | null = null;
  for (const run of runs) {
    const value = finite(run.summary?.[key]);
    if (value === undefined) continue;
    if (out === null || (dir === "min" ? value < out.value : value > out.value)) {
      out = { value, run: run.name };
    }
  }
  return out;
}

/**
 * Next sort for a header click, with the metric's declared goal deciding
 * which end a fresh metric sort opens on (lowest loss, highest accuracy).
 * A second click on the same column still just flips it.
 */
export function toggleSortWithGoals(
  current: RunSort | null,
  column: RunSortColumn,
  goals: MetricGoals,
): RunSort {
  const metric = column.startsWith("metric:") ? column.slice("metric:".length) : null;
  const goal = metric === null ? undefined : metricGoal(goals, metric);
  if (goal === undefined || current?.column === column) return toggleSort(current, column);
  return { column, dir: goal === "min" ? "asc" : "desc" };
}
