/**
 * Scale-side preparation for the uPlot metric charts: what a logarithmic y
 * axis may be handed, and whether a data update is a real one.
 *
 * Framework-free on purpose — `components/experiments/uplot-chart.tsx` is the
 * only caller, but the decisions here are the ones worth asserting in tests
 * rather than eyeballing on a canvas.
 */

/** uPlot's aligned data: `[xValues, ...yValuesPerSeries]`, null for a gap. */
export type ChartData = (number | null)[][];

/**
 * Is this value drawable on a logarithmic axis?
 *
 * uPlot does not skip a `0` or a negative value on a log scale: it clamps it
 * to `scaleMin / 10`, i.e. draws it below the axis at a position that is not
 * the value. A non-finite number is no better — it poisons the auto-range into
 * NaN. Both are lies a chart must not tell.
 */
function isLogPlottable(value: number | null): value is number {
  return value !== null && Number.isFinite(value) && value > 0;
}

export type LogScalePlan = {
  /**
   * Data to hand uPlot. Reference-identical to the input whenever nothing had
   * to be masked, so a memoised caller does not churn.
   */
  data: ChartData;
  /** Apply uPlot's log distribution (`distr: 3`) to the y scale. */
  logEnabled: boolean;
  /** Values replaced by a gap because a log axis cannot place them. */
  hiddenPoints: number;
  /**
   * Log was asked for but *no* y value is positive, so the chart falls back to
   * a linear axis. Without the fallback uPlot ranges the scale from
   * `[Infinity, -Infinity]` and paints nothing at all, with no error anywhere.
   */
  unavailable: boolean;
};

/**
 * Decide what a log-scale request can actually be given.
 *
 * - Not requested: everything passes through untouched.
 * - Requested, some positive values: values a log axis cannot place become
 *   gaps (`null`), and `hiddenPoints` says how many, so the UI can admit it.
 * - Requested, nothing positive: linear, with `unavailable` set. Silently
 *   drawing an empty chart is the failure this exists to prevent.
 */
export function planLogScale(data: ChartData, logScale: boolean): LogScalePlan {
  if (!logScale) return { data, logEnabled: false, hiddenPoints: 0, unavailable: false };

  let hiddenPoints = 0;
  let positives = 0;
  // Row 0 is x; only the y series are subject to the log y scale.
  for (let s = 1; s < data.length; s++) {
    for (const value of data[s] ?? []) {
      if (value === null) continue;
      if (isLogPlottable(value)) positives++;
      else hiddenPoints++;
    }
  }

  if (positives === 0) {
    return { data, logEnabled: false, hiddenPoints: 0, unavailable: true };
  }
  if (hiddenPoints === 0) {
    return { data, logEnabled: true, hiddenPoints: 0, unavailable: false };
  }

  const masked: ChartData = data.map((row, i) =>
    i === 0 ? row : row.map((value) => (isLogPlottable(value) ? value : null)),
  );
  return { data: masked, logEnabled: true, hiddenPoints, unavailable: false };
}

/**
 * Do two aligned data matrices hold the same numbers?
 *
 * The charts are re-rendered by things that have nothing to do with the data —
 * a keystroke in the metric filter, a tag select, the 15-second live poll —
 * and each render builds fresh arrays. Handing an equal-but-new array to
 * `uPlot.setData` is not free: the default re-ranges x, which throws away the
 * zoom the user dragged. So compare by value and skip the update entirely.
 *
 * `NaN` is treated as equal to itself (`Object.is`): it means "no reading" in
 * a metric series, and a series full of them must not read as changing on
 * every poll.
 */
export function chartDataEquals(a: ChartData, b: ChartData): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    const rowA = a[i] ?? [];
    const rowB = b[i] ?? [];
    if (rowA === rowB) continue;
    if (rowA.length !== rowB.length) return false;
    for (let j = 0; j < rowA.length; j++) {
      if (!Object.is(rowA[j], rowB[j])) return false;
    }
  }
  return true;
}
