import type { ExpMetricSeries } from "@/types/api";

const PALETTE = [
  "#5b8def",
  "#e8825a",
  "#5cb88a",
  "#c77dd1",
  "#e0b64f",
  "#4fb8c7",
  "#e2618a",
  "#8f9ce0",
  "#7ac74f",
  "#d97a3f",
];

export function colorForRun(index: number): string {
  return PALETTE[index % PALETTE.length] ?? PALETTE[0]!;
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
