import type { MetricGoals } from "@/lib/exp-goals";
import {
  defaultSidebarSort,
  primaryGoalMetric,
  runMatchesQuery,
  sidebarGroups,
} from "@/lib/exp-workspace";
import type { ExpRun } from "@/types/api";

/**
 * Pure rules behind the run page's previous / next / "jump to run" controls.
 *
 * The order is the project page's default run order — the run sidebar's
 * default sort, reused from `lib/exp-workspace.ts` rather than restated, so
 * stepping through runs here visits them in the order the sidebar lists them:
 *
 * 1. **Group first.** Runs of one sweep (`group`) stay together; the groups
 *    themselves, and ungrouped runs, are ordered by their leading run.
 * 2. **Best by the primary goal metric** when the project declares goals (the
 *    first of `orderedGoalMetrics`: held-out metrics such as `val/…` before
 *    the rest, alphabetical within), each run ranked by its *best* value — the
 *    lowest it reached for a "lower is better" goal, the highest otherwise. A
 *    run that never logged the metric sorts last.
 * 3. **Most recently updated first** otherwise.
 *
 * Archived runs are left out — the project page hides them by default — except
 * the run being viewed, which always keeps its place so the reader is never on
 * a page the switcher cannot find.
 */

/** The metric the order ranks by, or undefined for "most recent first". */
export function runPagePrimaryMetric(
  runs: readonly ExpRun[],
  goals: MetricGoals,
): string | undefined {
  return primaryGoalMetric(
    goals,
    runs.filter((run) => !run.archived),
  );
}

/**
 * Run names in the project's reading order (see the file comment). `current`
 * is kept even when archived.
 */
export function runPageOrder(
  runs: readonly ExpRun[],
  goals: MetricGoals,
  current?: string,
): string[] {
  const listed = runs.filter((run) => !run.archived || run.name === current);
  const sort = defaultSidebarSort(runPagePrimaryMetric(runs, goals));
  return sidebarGroups(listed, sort, goals).flatMap((group) => group.runs.map((run) => run.name));
}

export type RunNeighbours = {
  prev?: string;
  next?: string;
  /** 0-based position of the current run, -1 when it is not in the order. */
  index: number;
  total: number;
};

/** The runs either side of `current`. No wrap-around: the ends are ends. */
export function runNeighbours(order: readonly string[], current: string): RunNeighbours {
  const index = order.indexOf(current);
  if (index < 0) return { index, total: order.length };
  return {
    prev: index > 0 ? order[index - 1] : undefined,
    next: index < order.length - 1 ? order[index + 1] : undefined,
    index,
    total: order.length,
  };
}

/**
 * The runs the "jump to run" picker lists for a query — the same matching as
 * the project page's run filter (every whitespace-separated term appears in
 * the name, group, job type or a tag). Order is preserved.
 */
export function filterRunsForPicker(runs: readonly ExpRun[], query: string): ExpRun[] {
  return runs.filter((run) => runMatchesQuery(run, query));
}

/** What a key press on the run page asks for, or null for "not ours". */
export type RunPageShortcut = "prev" | "next";

/**
 * Maps a keydown to a run-page shortcut. `[` / `k` go to the previous run and
 * `]` / `j` to the next one; anything with a modifier (other than the Shift a
 * layout may need for a bracket) is left to the browser.
 */
export function runPageShortcut(e: {
  key: string;
  altKey: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
}): RunPageShortcut | null {
  if (e.altKey || e.ctrlKey || e.metaKey) return null;
  switch (e.key) {
    case "[":
    case "k":
      return "prev";
    case "]":
    case "j":
      return "next";
    default:
      return null;
  }
}

/**
 * True when a key press belongs to whatever has focus: a text field, a select,
 * a contenteditable region. Shortcuts must never hijack typing.
 */
export function isTypingTarget(target: EventTarget | null): boolean {
  if (typeof Element === "undefined" || !(target instanceof Element)) return false;
  if (target.closest("input, textarea, select, [contenteditable=''], [contenteditable='true']")) {
    return true;
  }
  return false;
}

/**
 * The project page, optionally with some runs selected. The project page keeps
 * its selection in the URL as repeated `?runs=` parameters (one per name, so a
 * name with a comma survives — `lib/exp-workspace.ts`), so "back to project"
 * can land with the run the reader just looked at already plotted.
 */
export function projectHref(
  ns: string,
  repo: string,
  project: string,
  selected: readonly string[] = [],
): string {
  const base = `/experiments/${encodeURIComponent(ns)}/${encodeURIComponent(repo)}/${encodeURIComponent(project)}`;
  const params = new URLSearchParams();
  for (const name of new Set(selected.filter(Boolean))) params.append("runs", name);
  const qs = params.toString();
  return qs ? `${base}?${qs}` : base;
}
