import { isReservedConfigKey } from "@/lib/run-config";
import type { ExpRun } from "@/types/api";

/**
 * Pure geometry behind the parallel-coordinates plot: the sweep view that
 * answers "which corner of the search space did the good runs come from?" in
 * one picture, where a stack of training curves cannot.
 *
 * Every axis is normalised to a *fraction* here (0 at the bottom of the axis,
 * 1 at the top) and nothing knows about pixels, so the placement rules — how a
 * categorical axis is laid out, what happens to a constant hyperparameter,
 * where a run with a missing value goes — are testable without a DOM.
 */

/** Metric keys under this prefix are machine telemetry, not results. */
const SYSTEM_PREFIX = "system/";

/**
 * A categorical axis with more distinct values than this is not a
 * hyperparameter anyone swept — it is an identifier (an output directory, a
 * seed written as a string) — and it would draw one tick per run. Such a key
 * is left out of the candidate axes entirely.
 */
export const MAX_AXIS_CATEGORIES = 12;

export type ParallelAxis = {
  /** Stable identifier, e.g. "config:lr" or "metric:loss". */
  id: string;
  label: string;
  source: "config" | "metric";
  key: string;
} & (
  | {
      kind: "numeric";
      /** Range covered by the runs on screen; equal when the value is constant. */
      min: number;
      max: number;
    }
  | {
      kind: "categorical";
      /** Distinct values, sorted; the position of a value is its index. */
      categories: string[];
    }
);

function axisId(source: "config" | "metric", key: string): string {
  return `${source}:${key}`;
}

/**
 * Renders a categorical config value. Only scalars become categories: an
 * object or an array is a nested config block, which has no single position on
 * an axis and is skipped.
 */
function categoryOf(value: unknown): string | null {
  if (typeof value === "string") return value;
  if (typeof value === "boolean") return String(value);
  return null;
}

/**
 * The axes available for a set of runs.
 *
 * Config keys come first (they are the inputs) and metrics second (the
 * outputs), each alphabetical, which makes the default left-to-right reading
 * "hyperparameters, then what they produced". A key that is numeric in one run
 * and a string in another is treated as categorical: mixing the two on one
 * scale would place "auto" somewhere among the learning rates.
 *
 * `_meta` / `_args` keys are excluded for the same reason the config diff
 * table folds them away: every run differs on the git commit and half the
 * TrainingArguments, which is never what a sweep was varying.
 */
export function parallelAxes(runs: ExpRun[]): ParallelAxis[] {
  const numbers = new Map<string, number[]>();
  const categories = new Map<string, Set<string>>();

  for (const run of runs) {
    for (const [key, value] of Object.entries(run.config ?? {})) {
      if (isReservedConfigKey(key)) continue;
      if (typeof value === "number" && Number.isFinite(value)) {
        const list = numbers.get(key) ?? [];
        list.push(value);
        numbers.set(key, list);
        continue;
      }
      const category = categoryOf(value);
      if (category === null) continue;
      const set = categories.get(key) ?? new Set<string>();
      set.add(category);
      categories.set(key, set);
    }
  }

  const configAxes: ParallelAxis[] = [];
  const keys = new Set([...numbers.keys(), ...categories.keys()]);
  for (const key of Array.from(keys).sort((a, b) => a.localeCompare(b))) {
    const cats = categories.get(key);
    const nums = numbers.get(key);
    if (cats && cats.size > 0) {
      // Mixed key: the numbers join the categories as their own labels, so no
      // run silently loses its value on this axis.
      const all = new Set(cats);
      for (const n of nums ?? []) all.add(String(n));
      if (all.size > MAX_AXIS_CATEGORIES) continue;
      configAxes.push({
        id: axisId("config", key),
        label: key,
        source: "config",
        key,
        kind: "categorical",
        categories: Array.from(all).sort((a, b) => a.localeCompare(b)),
      });
      continue;
    }
    if (!nums || nums.length === 0) continue;
    configAxes.push({
      id: axisId("config", key),
      label: key,
      source: "config",
      key,
      kind: "numeric",
      min: Math.min(...nums),
      max: Math.max(...nums),
    });
  }

  const metricValues = new Map<string, number[]>();
  for (const run of runs) {
    for (const [key, value] of Object.entries(run.summary ?? {})) {
      if (key.startsWith(SYSTEM_PREFIX)) continue;
      if (typeof value !== "number" || !Number.isFinite(value)) continue;
      const list = metricValues.get(key) ?? [];
      list.push(value);
      metricValues.set(key, list);
    }
  }
  const metricAxes: ParallelAxis[] = Array.from(metricValues.keys())
    .sort((a, b) => a.localeCompare(b))
    .map((key) => {
      const values = metricValues.get(key) ?? [];
      return {
        id: axisId("metric", key),
        label: key,
        source: "metric" as const,
        key,
        kind: "numeric" as const,
        min: Math.min(...values),
        max: Math.max(...values),
      };
    });

  return [...configAxes, ...metricAxes];
}

/** One run's position on one axis. `t` is 0 at the bottom, 1 at the top. */
export type AxisPoint = { t: number; label: string };

function formatNumeric(value: number): string {
  // Magnitude decides the notation before integer-ness does: an integer-valued
  // metric like a `huge` counter (1.85e13) is still a wall of digits nobody
  // can read on an axis tick, and must fall back to exponential the same way
  // a non-integer of that size would (matches `formatMetricValue`,
  // lib/experiments.ts, which orders the same two checks the same way).
  const abs = Math.abs(value);
  if (abs !== 0 && (abs < 1e-3 || abs >= 1e6)) return value.toExponential(2);
  if (Number.isInteger(value)) return String(value);
  return String(Number(value.toPrecision(4)));
}

/**
 * Where one run sits on one axis, or null when it has no value there.
 *
 * A constant axis (every run logged the same learning rate, or a categorical
 * axis with one category) puts every run in the middle rather than at the
 * bottom: the line should read as "no variation here", not as "worst possible".
 */
export function axisPoint(run: ExpRun, axis: ParallelAxis): AxisPoint | null {
  const raw = axis.source === "config" ? run.config?.[axis.key] : run.summary?.[axis.key];
  if (raw === undefined || raw === null) return null;

  if (axis.kind === "categorical") {
    const label = typeof raw === "number" ? String(raw) : categoryOf(raw);
    if (label === null) return null;
    const index = axis.categories.indexOf(label);
    if (index < 0) return null;
    const span = axis.categories.length - 1;
    return { t: span <= 0 ? 0.5 : index / span, label };
  }

  if (typeof raw !== "number" || !Number.isFinite(raw)) return null;
  const span = axis.max - axis.min;
  return { t: span === 0 ? 0.5 : (raw - axis.min) / span, label: formatNumeric(raw) };
}

/** Tick labels for an axis, bottom first. */
export function axisTicks(axis: ParallelAxis): string[] {
  if (axis.kind === "categorical") return axis.categories;
  if (axis.min === axis.max) return [formatNumeric(axis.min)];
  return [formatNumeric(axis.min), formatNumeric(axis.max)];
}

export type ParallelLine = {
  run: string;
  /** Positions in axis order; an axis the run has no value for is omitted. */
  points: { axis: number; t: number; label: string }[];
  /** False when the run is missing a value on at least one selected axis. */
  complete: boolean;
};

/**
 * One polyline per run. A run missing a value on some axis keeps the segments
 * it does have (drawn dashed by the component) rather than being dropped: with
 * a metric only the finished runs logged, dropping would empty the plot.
 * A run with fewer than two positions has nothing to draw and is left out.
 */
export function parallelLines(runs: ExpRun[], axes: ParallelAxis[]): ParallelLine[] {
  const out: ParallelLine[] = [];
  for (const run of runs) {
    const points: ParallelLine["points"] = [];
    for (const [index, axis] of axes.entries()) {
      const point = axisPoint(run, axis);
      if (point) points.push({ axis: index, t: point.t, label: point.label });
    }
    if (points.length < 2) continue;
    out.push({ run: run.name, points, complete: points.length === axes.length });
  }
  return out;
}

/**
 * X position of axis `index` inside a plot of `width` units with `pad` on each
 * side. A single axis is centred rather than pinned to the left edge.
 */
export function axisX(index: number, count: number, width: number, pad: number): number {
  if (count <= 1) return width / 2;
  return pad + (index * (width - 2 * pad)) / (count - 1);
}

/** Y position of a fraction inside a plot of `height` units with `pad`. */
export function axisY(t: number, height: number, pad: number): number {
  return height - pad - t * (height - 2 * pad);
}

/** The `d` of one run's polyline, in the same units as axisX / axisY. */
export function linePath(
  line: ParallelLine,
  axisCount: number,
  width: number,
  height: number,
  pad: number,
): string {
  return line.points
    .map((p, i) => {
      const x = axisX(p.axis, axisCount, width, pad);
      const y = axisY(p.t, height, pad);
      return `${i === 0 ? "M" : "L"}${x.toFixed(2)} ${y.toFixed(2)}`;
    })
    .join(" ");
}
