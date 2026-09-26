import { effectiveDirection, type MetricGoals, metricGoal, summaryValue } from "@/lib/exp-goals";
import { groupRuns, type RunGroup } from "@/lib/run-grouping";
import type { ExpRun, RunStatus } from "@/types/api";
import { RunStatusFailed, RunStatusFinished, RunStatusRunning, RunStatusStale } from "@/types/api";

/**
 * Pure rules behind the experiment project workspace: the run sidebar (search,
 * status filter, sort, grouping), the runs selected when the page opens, the
 * order the charts come in, and the `?view=` / `?runs=` URL state.
 *
 * Data-in / data-out so every rule is testable without a DOM; the components
 * only decide where the results go.
 */

// ---------------------------------------------------------------- views

export const WORKSPACE_VIEWS = [
  "charts",
  "table",
  "config",
  "scatter",
  "parallel",
  "notes",
] as const;
export type WorkspaceView = (typeof WORKSPACE_VIEWS)[number];

/** What the page opens on when the URL names no view (or an unknown one). */
export const DEFAULT_VIEW: WorkspaceView = "charts";

export function parseView(raw: string | string[] | undefined): WorkspaceView {
  const value = Array.isArray(raw) ? raw[0] : raw;
  return (WORKSPACE_VIEWS as readonly string[]).includes(value ?? "")
    ? (value as WorkspaceView)
    : DEFAULT_VIEW;
}

// ---------------------------------------------------------- goal metrics

/** Machine telemetry, never a result anyone optimises for. */
const SYSTEM_PREFIX = "system/";

/**
 * A metric measured on held-out data. Those are what a project's goal usually
 * means — "train/loss ↓" is a goal too, but nobody picks the best model by it.
 */
const HELD_OUT = /(^|[._/-])(val|valid|validation|eval|test|dev)([._/-]|$)/i;

function hasMetric(run: ExpRun, key: string): boolean {
  return (
    key in (run.summary ?? {}) || key in (run.summary_min ?? {}) || key in (run.summary_max ?? {})
  );
}

/**
 * The project's goal metrics in the order the workspace presents them: only
 * metrics some run has actually logged (a goal left over from a renamed metric
 * has nothing to show), `system/*` never, held-out metrics (`val`, `eval`,
 * `test`, `dev` as a name segment) before the rest, alphabetical within each.
 */
export function orderedGoalMetrics(goals: MetricGoals, runs: readonly ExpRun[]): string[] {
  return Object.keys(goals)
    .filter((key) => metricGoal(goals, key) !== undefined)
    .filter((key) => !key.startsWith(SYSTEM_PREFIX))
    .filter((key) => runs.some((run) => hasMetric(run, key)))
    .sort((a, b) => {
      const ha = HELD_OUT.test(a) ? 0 : 1;
      const hb = HELD_OUT.test(b) ? 0 : 1;
      return ha - hb || a.localeCompare(b);
    });
}

/**
 * The one metric the workspace leads with — the sidebar's default sort, the
 * "best" fact in the header, the scatter plot's default y axis: the first of
 * `orderedGoalMetrics`, or undefined for a project without goals.
 */
export function primaryGoalMetric(goals: MetricGoals, runs: readonly ExpRun[]): string | undefined {
  return orderedGoalMetrics(goals, runs)[0];
}

/**
 * A run's value for a metric as the sidebar ranks it: its best value in the
 * metric's direction (`summary_min` for "lower is better"), which is also what
 * the server's trophy is decided on.
 */
export function bestValue(run: ExpRun, metric: string, goals: MetricGoals): number | undefined {
  return summaryValue(run, metric, "best", goals);
}

/** Runs that have a value for `metric`, best first; ties by name. */
export function rankRunsByMetric(
  runs: readonly ExpRun[],
  metric: string,
  goals: MetricGoals,
): ExpRun[] {
  const dir = effectiveDirection(goals, metric);
  return runs
    .map((run) => ({ run, value: bestValue(run, metric, goals) }))
    .filter((entry): entry is { run: ExpRun; value: number } => entry.value !== undefined)
    .sort((a, b) => {
      if (a.value !== b.value) return dir === "min" ? a.value - b.value : b.value - a.value;
      return a.run.name.localeCompare(b.run.name);
    })
    .map((entry) => entry.run);
}

/** The best value a set of runs reached for a metric, and who reached it. */
export function bestOf(
  runs: readonly ExpRun[],
  metric: string,
  goals: MetricGoals,
): { run: string; value: number } | null {
  const [top] = rankRunsByMetric(runs, metric, goals);
  if (!top) return null;
  const value = bestValue(top, metric, goals);
  return value === undefined ? null : { run: top.name, value };
}

// ----------------------------------------------------- default selection

/** The most runs the page pre-selects. */
export const DEFAULT_SELECTION_LIMIT = 6;
/** How many runs "most recently updated" falls back to. */
export const DEFAULT_SELECTION_FALLBACK = 5;
/** Running runs taken first when a goal also wants room for the best runs. */
const RUNNING_FIRST = 3;

function byUpdatedDesc(a: ExpRun, b: ExpRun): number {
  const d = Date.parse(b.updated_at) - Date.parse(a.updated_at);
  return Number.isNaN(d) || d === 0 ? a.name.localeCompare(b.name) : d;
}

/**
 * The runs selected when the page opens without a `?runs=` in the URL — the
 * answer to "how are the runs going right now, and which one is best?"
 * without a click.
 *
 * 1. Archived runs are never picked.
 * 2. Running runs first, most recently heard from first — up to 3 when the
 *    project has a goal metric (so the best runs still get room), up to the
 *    limit (6) otherwise.
 * 3. Then the best runs by the primary goal metric, until the limit.
 * 4. Then any running runs step 2 left out, until the limit.
 * 5. A project with no goal metric tops up with the most recently updated
 *    runs until it has 5; one with nothing picked at all gets those 5 too.
 *
 * Names are returned in the order of `runs` (the project's colour order), so
 * the selection reads the same wherever it is printed.
 */
export function defaultRunSelection(
  runs: readonly ExpRun[],
  goals: MetricGoals,
  limit = DEFAULT_SELECTION_LIMIT,
): string[] {
  const active = runs.filter((run) => !run.archived);
  const picked = new Set<string>();
  const add = (list: readonly ExpRun[], cap: number) => {
    for (const run of list) {
      if (picked.size >= cap) return;
      picked.add(run.name);
    }
  };

  const running = active.filter((run) => run.status === RunStatusRunning).sort(byUpdatedDesc);
  const primary = primaryGoalMetric(goals, active);
  const ranked = primary ? rankRunsByMetric(active, primary, goals) : [];

  add(running, ranked.length > 0 ? Math.min(RUNNING_FIRST, limit) : limit);
  add(ranked, limit);
  add(running, limit);
  if (ranked.length === 0 || picked.size === 0) {
    add([...active].sort(byUpdatedDesc), Math.max(picked.size, DEFAULT_SELECTION_FALLBACK));
  }

  return runs.filter((run) => picked.has(run.name)).map((run) => run.name);
}

// ---------------------------------------------------- sidebar filtering

/**
 * Case-insensitive match of a free-text query against a run's name, group,
 * job type and tags. Whitespace separates terms and every term must match
 * somewhere, so "e6 oom" finds `e6_all_oom` and "lr-sweep bs64" the batch-64
 * members of the lr sweep.
 */
export function runMatchesQuery(run: ExpRun, query: string): boolean {
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (terms.length === 0) return true;
  const haystack = [run.name, run.group, run.job_type, ...(run.tags ?? [])]
    .filter(Boolean)
    .map((s) => s.toLowerCase());
  return terms.every((term) => haystack.some((s) => s.includes(term)));
}

export const STATUS_FILTERS = [
  "all",
  RunStatusRunning,
  RunStatusStale,
  RunStatusFinished,
  RunStatusFailed,
] as const;
export type StatusFilter = (typeof STATUS_FILTERS)[number];

export function runMatchesStatus(run: ExpRun, status: StatusFilter): boolean {
  return status === "all" || run.status === status;
}

/** How many of `runs` each status filter would keep. */
export function countByStatus(runs: readonly ExpRun[]): Record<StatusFilter, number> {
  const out: Record<StatusFilter, number> = {
    all: runs.length,
    [RunStatusRunning]: 0,
    [RunStatusStale]: 0,
    [RunStatusFinished]: 0,
    [RunStatusFailed]: 0,
  } as Record<StatusFilter, number>;
  for (const run of runs) {
    const status: RunStatus = run.status;
    out[status] = (out[status] ?? 0) + 1;
  }
  return out;
}

// ------------------------------------------------------- sidebar sorting

/** How the run sidebar orders its rows. */
export type SidebarSort =
  | { kind: "metric"; metric: string }
  | { kind: "updated" }
  | { kind: "name" };

/** The `<select>` value for a sort. */
export function sidebarSortValue(sort: SidebarSort): string {
  return sort.kind === "metric" ? `metric:${sort.metric}` : sort.kind;
}

export function parseSidebarSort(value: string): SidebarSort {
  if (value.startsWith("metric:")) return { kind: "metric", metric: value.slice("metric:".length) };
  return value === "name" ? { kind: "name" } : { kind: "updated" };
}

/** Best by the primary goal metric when there is one, newest otherwise. */
export function defaultSidebarSort(primary: string | undefined): SidebarSort {
  return primary ? { kind: "metric", metric: primary } : { kind: "updated" };
}

/**
 * Compares two runs for the sidebar. A run without a value for the sort metric
 * sorts last whichever way the metric points: "never logged a CER" is not
 * "a CER of zero".
 */
export function compareForSidebar(
  a: ExpRun,
  b: ExpRun,
  sort: SidebarSort,
  goals: MetricGoals,
): number {
  switch (sort.kind) {
    case "name":
      return a.name.localeCompare(b.name);
    case "updated":
      return byUpdatedDesc(a, b);
    case "metric": {
      const av = bestValue(a, sort.metric, goals);
      const bv = bestValue(b, sort.metric, goals);
      if (av === undefined && bv === undefined) return a.name.localeCompare(b.name);
      if (av === undefined) return 1;
      if (bv === undefined) return -1;
      if (av === bv) return a.name.localeCompare(b.name);
      const dir = effectiveDirection(goals, sort.metric);
      return dir === "min" ? av - bv : bv - av;
    }
  }
}

/**
 * The sidebar's rows: runs bucketed by their declared group (a run with no
 * group is a row of its own), members sorted, and groups ordered by their own
 * leading member — so "best CER" puts the sweep that found the lowest CER on
 * top. Sorting by name orders groups by their name rather than their first
 * member's.
 */
export function sidebarGroups(
  runs: readonly ExpRun[],
  sort: SidebarSort,
  goals: MetricGoals,
): RunGroup[] {
  const groups = groupRuns([...runs]).map((group) => ({
    ...group,
    runs: [...group.runs].sort((a, b) => compareForSidebar(a, b, sort, goals)),
  }));
  return groups.sort((a, b) => {
    if (sort.kind === "name") {
      const ak = a.grouped ? a.key : (a.runs[0]?.name ?? "");
      const bk = b.grouped ? b.key : (b.runs[0]?.name ?? "");
      return ak.localeCompare(bk);
    }
    const [ar] = a.runs;
    const [br] = b.runs;
    if (!ar || !br) return 0;
    return compareForSidebar(ar, br, sort, goals);
  });
}

// ------------------------------------------------------------- charts

/**
 * The order the metric charts come in: goal metrics first (in `goalOrder`),
 * then the rest alphabetically, with `system/*` split off into its own list
 * (the workspace folds it away).
 */
export function orderChartKeys(
  keys: readonly string[],
  goalOrder: readonly string[],
): { main: string[]; system: string[] } {
  const unique = Array.from(new Set(keys));
  const rank = new Map(goalOrder.map((key, i) => [key, i]));
  const main = unique
    .filter((key) => !key.startsWith(SYSTEM_PREFIX))
    .sort((a, b) => {
      const ra = rank.get(a) ?? Number.POSITIVE_INFINITY;
      const rb = rank.get(b) ?? Number.POSITIVE_INFINITY;
      if (ra !== rb) return ra - rb;
      return a.localeCompare(b);
    });
  const system = unique
    .filter((key) => key.startsWith(SYSTEM_PREFIX))
    .sort((a, b) => a.localeCompare(b));
  return { main, system };
}

// ----------------------------------------------------------- URL state

/**
 * Past this many characters of encoded `runs=` parameters the selection is not
 * written to the URL at all. A partial list would reload as a different
 * selection than the one on screen; leaving it out reloads as the default,
 * which at least says nothing untrue. The selection itself stays in memory.
 */
export const MAX_URL_RUNS_CHARS = 1500;

/**
 * The selection a URL asks for: null when it has no `runs` parameter (use the
 * default), `[]` for an explicit empty selection (`?runs=`), the names
 * otherwise. Each name is its own repeated parameter, so a run literally named
 * `lr=0.1,bs=32` survives the round trip (the same reason the metrics API
 * takes repeated `run=`).
 */
export function parseRunsParam(raw: string | string[] | undefined): string[] | null {
  if (raw === undefined) return null;
  const values = Array.isArray(raw) ? raw : [raw];
  return Array.from(new Set(values.filter((value) => value !== "")));
}

/**
 * The query string for the workspace's current state, starting from `current`
 * (the page's existing search string) so parameters this page does not own
 * survive. `selected` is null to leave `runs` untouched (the reader has not
 * changed the selection yet); names are written in `runOrder` order so the
 * same selection always produces the same URL.
 */
export function workspaceSearch(
  current: string,
  view: WorkspaceView,
  selected: readonly string[] | null,
  runOrder: readonly string[],
): string {
  const params = new URLSearchParams(current);
  if (view === DEFAULT_VIEW) params.delete("view");
  else params.set("view", view);

  if (selected !== null) {
    params.delete("runs");
    const chosen = new Set(selected);
    const ordered = runOrder.filter((name) => chosen.has(name));
    const encoded = ordered.map((name) => `runs=${encodeURIComponent(name)}`).join("&");
    if (ordered.length === 0) params.append("runs", "");
    else if (encoded.length <= MAX_URL_RUNS_CHARS) {
      for (const name of ordered) params.append("runs", name);
    }
  }

  const out = params.toString();
  return out ? `?${out}` : "";
}
