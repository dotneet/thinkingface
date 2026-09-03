import type { ExpMetricSeries } from "@/types/api";

// 20 evenly-spaced hues (alternating lightness for extra separation between
// neighbours) rather than the original 10: a sweep's "select all" routinely
// puts 11+ runs on one chart, and a 10-colour palette silently reuses colour
// 1 for run 11 — the line, legend chip, table dot and config-diff header all
// read as the same run. 20 pushes that collision out to a size sweeps rarely
// reach; `dashForRun` below covers what is left once a comparison does grow
// past even that.
const PALETTE = [
  "#d46e5e",
  "#cc7b3e",
  "#d4b55e",
  "#c7cc3e",
  "#add45e",
  "#72cc3e",
  "#66d45e",
  "#3ecc5f",
  "#5ed49d",
  "#3eccb4",
  "#5ec4d4",
  "#3e8ecc",
  "#5e7dd4",
  "#423ecc",
  "#855ed4",
  "#983ecc",
  "#cc5ed4",
  "#cc3eaa",
  "#d45e95",
  "#cc3e55",
];

export function colorForRun(index: number): string {
  return PALETTE[index % PALETTE.length] ?? PALETTE[0]!;
}

/**
 * Dash pattern for a run's line, undefined for a solid stroke. Colour alone
 * stops being able to tell two runs apart the moment a comparison grows past
 * `PALETTE.length` runs (colour wraps back to index 0's hue) — so every extra
 * lap around the palette also switches dash pattern, and a run that shares a
 * colour with an earlier one almost never shares its dash too. The first lap
 * (the overwhelmingly common case: a handful of runs, or a sweep under the
 * palette size) stays solid, matching the chart's look before this existed.
 *
 * A run marked as the baseline overrides this with its own dedicated dash
 * (see `BASELINE_DASH` in metrics-charts.tsx) — callers apply that after
 * calling this, not instead of it, so a wrapped-palette run that is also the
 * baseline still reads as "the baseline" first.
 */
const DASH_PATTERNS: (number[] | undefined)[] = [undefined, [5, 3], [1, 3], [8, 3, 1, 3]];

export function dashForRun(index: number): number[] | undefined {
  if (index < 0) return undefined;
  const lap = Math.floor(index / PALETTE.length) % DASH_PATTERNS.length;
  return DASH_PATTERNS[lap];
}

/**
 * Whether a chart in the given mode should draw its line through the null
 * gaps `alignSeriesForKey` fills in, rather than break the line there
 * (uPlot's `spanGaps` option).
 *
 * `alignSeriesForKey` unions every plotted run's x values and fills whatever
 * a run did not report at a given x with null. When every run is sampled at
 * the same x positions, that null is a genuine gap. But the backend
 * downsamples each run to `max_points` independently
 * (backend/internal/experiments/series.go), so two runs being compared
 * almost never share a sampling stride once either has passed that cap —
 * most of the nulls this app produces mean "this run wasn't sampled here",
 * not "this run has no data here". uPlot's default (`spanGaps: false`, and
 * this app draws points-off line charts) makes every one of those isolated
 * nulls fully invisible: no line segment, no marker — a run whose stride
 * doesn't line up with the others' can vanish outright (see uplot-chart.tsx).
 * Spanning the gap draws a straight line across it instead, which can
 * occasionally paper over a true gap in one run's own logging — but that is
 * a far smaller misread than a run's line disappearing depending on which
 * other runs happen to be selected alongside it.
 *
 * Scatter mode has no line at all (`paths: () => null` in uplot-chart.tsx),
 * so the option is meaningless there.
 */
export function spanGapsForMode(mode: "line" | "scatter"): boolean {
  return mode === "line";
}

/**
 * Combines multiple runs' point series for a single metric key onto a
 * shared, sorted x-axis so uPlot can render them as aligned data. Missing
 * points become null (gap).
 */
export function alignSeriesForKey(
  seriesForKey: { run: string; points: [number, number][] }[],
): (number | null)[][] {
  const xSet = new Set<number>();
  for (const s of seriesForKey) {
    for (const [x] of s.points) xSet.add(x);
  }
  const xs = Array.from(xSet).sort((a, b) => a - b);
  const xIndex = new Map(xs.map((x, i) => [x, i]));

  const rows: (number | null)[][] = [xs, ...seriesForKey.map(() => xs.map(() => null))];
  seriesForKey.forEach((s, seriesIdx) => {
    const row = rows[seriesIdx + 1]!;
    for (const [x, y] of s.points) {
      const idx = xIndex.get(x);
      if (idx !== undefined) row[idx] = y;
    }
  });
  return rows;
}

/** Exponential moving average smoothing; nulls pass through as gaps but do
 * not reset the running average (matches typical training-curve smoothing
 * behavior in tools like TensorBoard). */
export function emaSmooth(values: (number | null)[], alpha: number): (number | null)[] {
  if (alpha <= 0) return values;
  let ema: number | null = null;
  return values.map((v) => {
    if (v === null) return null;
    ema = ema === null ? v : alpha * ema + (1 - alpha) * v;
    return ema;
  });
}

export function groupByKey(
  series: ExpMetricSeries[],
): Map<string, { run: string; points: [number, number][] }[]> {
  const map = new Map<string, { run: string; points: [number, number][] }[]>();
  for (const s of series) {
    const list = map.get(s.key) ?? [];
    list.push({ run: s.run, points: s.points });
    map.set(s.key, list);
  }
  return map;
}

/**
 * `run name -> its position in the project's run order`, which is what
 * `colorForRun` wants as its argument.
 *
 * The colour of a run is decided by where it sits in the project's full run
 * order, so every dot next to a run name needs that index. Looked up with
 * `runOrder.indexOf(name)` per dot it costs a scan of the whole project per
 * row — O(n²) over a long run list, redone on every render. Build the map
 * once instead (`useMemo(() => runColorIndex(runOrder), [runOrder])`) and
 * hand it to `RunColorDot`.
 *
 * A duplicate name keeps its first position, matching `indexOf`; a name that
 * is not in the order is absent, and callers fall back to -1 exactly as
 * `indexOf` would.
 */
export function runColorIndex(runOrder: readonly string[]): ReadonlyMap<string, number> {
  const map = new Map<string, number>();
  runOrder.forEach((run, i) => {
    if (!map.has(run)) map.set(run, i);
  });
  return map;
}
