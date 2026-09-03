import type { ExpRun } from "@/types/api";

/**
 * Pure helpers behind the run comparison UI (config diff table, scatter plot,
 * tag filtering). Everything here is data-in / data-out so the rules — what
 * counts as "different", what may go on an axis — are testable without a DOM.
 */

/** One row of the config diff table: a key and its value in each run. */
export type ConfigDiffRow = {
  key: string;
  /** Formatted values, one per run, in the same order as the runs passed in. */
  values: string[];
  /** True when at least two runs disagree (a missing value counts as a value). */
  differs: boolean;
};

/** The marker used in the diff table for a key a run does not define. */
export const MISSING = "—";

/**
 * Renders a config value for display and for comparison. Objects and arrays go
 * through JSON so nested hyperparameters (an optimizer block, a layer list)
 * still compare structurally rather than all collapsing to "[object Object]".
 */
export function formatConfigValue(value: unknown): string {
  if (value === undefined) return MISSING;
  if (value === null) return "null";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  try {
    return JSON.stringify(value) ?? String(value);
  } catch {
    return String(value);
  }
}

/**
 * Builds the config comparison table for the given runs. Keys are the union of
 * every run's config, sorted so the table is stable as the selection changes;
 * a key one run omits shows as MISSING and counts as a difference.
 */
export function buildConfigDiff(runs: ExpRun[]): ConfigDiffRow[] {
  const keys = new Set<string>();
  for (const run of runs) {
    for (const key of Object.keys(run.config ?? {})) keys.add(key);
  }
  return Array.from(keys)
    .sort((a, b) => a.localeCompare(b))
    .map((key) => {
      const values = runs.map((run) => formatConfigValue(run.config?.[key]));
      const differs = values.some((v) => v !== values[0]);
      return { key, values, differs };
    });
}

/** An axis a run scatter plot can be drawn against. */
export type ScatterAxis = {
  /** Stable identifier, e.g. "config:lr" or "metric:loss". */
  id: string;
  source: "config" | "metric";
  key: string;
};

export function axisId(source: ScatterAxis["source"], key: string): string {
  return `${source}:${key}`;
}

/**
 * Human-readable label for a scatter axis, e.g. "config: lr" — the prefix
 * disambiguates a config key from a metric of the same name across the axis
 * `<select>`s, the chart's axis titles and its title (`run-scatter.tsx`).
 * This module stays framework- and i18n-free on purpose (see the file
 * header), so the caller passes in the already-translated prefix for each
 * source rather than this reaching into the dictionary itself.
 */
export function axisLabel(
  axis: Pick<ScatterAxis, "source" | "key">,
  prefixes: { config: string; metric: string },
): string {
  return `${axis.source === "config" ? prefixes.config : prefixes.metric}: ${axis.key}`;
}

/**
 * Numeric axes available for the given runs: every config key that is numeric
 * in at least one run, plus every metric with a final value. A config key that
 * is only ever a string (a model name, say) is left out — it cannot be plotted.
 */
export function scatterAxes(runs: ExpRun[]): ScatterAxis[] {
  const configKeys = new Set<string>();
  const metricKeys = new Set<string>();
  for (const run of runs) {
    for (const [key, value] of Object.entries(run.config ?? {})) {
      if (typeof value === "number" && Number.isFinite(value)) configKeys.add(key);
    }
    for (const [key, value] of Object.entries(run.summary ?? {})) {
      if (typeof value === "number" && Number.isFinite(value)) metricKeys.add(key);
    }
  }
  const sorted = (set: Set<string>) => Array.from(set).sort((a, b) => a.localeCompare(b));
  return [
    ...sorted(configKeys).map((key) => ({
      id: axisId("config", key),
      source: "config" as const,
      key,
    })),
    ...sorted(metricKeys).map((key) => ({
      id: axisId("metric", key),
      source: "metric" as const,
      key,
    })),
  ];
}

/** Reads one run's value for an axis, or null when the run has no such value. */
export function axisValue(run: ExpRun, axis: ScatterAxis | undefined): number | null {
  if (!axis) return null;
  const raw = axis.source === "config" ? run.config?.[axis.key] : run.summary?.[axis.key];
  return typeof raw === "number" && Number.isFinite(raw) ? raw : null;
}

export type ScatterPoint = { run: string; x: number; y: number; isBaseline: boolean };

/**
 * Points for the run scatter plot. Runs missing either coordinate are dropped
 * rather than drawn at zero, which would invent a data point that never ran.
 */
export function scatterPoints(
  runs: ExpRun[],
  x: ScatterAxis | undefined,
  y: ScatterAxis | undefined,
): ScatterPoint[] {
  const out: ScatterPoint[] = [];
  for (const run of runs) {
    const xv = axisValue(run, x);
    const yv = axisValue(run, y);
    if (xv === null || yv === null) continue;
    out.push({ run: run.name, x: xv, y: yv, isBaseline: run.is_baseline });
  }
  return out;
}

/** Every tag in use across the given runs, sorted, without duplicates. */
export function allTags(runs: ExpRun[]): string[] {
  const tags = new Set<string>();
  for (const run of runs) {
    for (const tag of run.tags ?? []) tags.add(tag);
  }
  return Array.from(tags).sort((a, b) => a.localeCompare(b));
}

export type RunFilter = {
  /** Include archived runs. Off by default: archiving is meant to hide noise. */
  showArchived: boolean;
  /** When set, keep only runs carrying this tag. */
  tag?: string;
};

export function filterRuns(runs: ExpRun[], filter: RunFilter): ExpRun[] {
  return runs.filter((run) => {
    if (!filter.showArchived && run.archived) return false;
    if (filter.tag && !(run.tags ?? []).includes(filter.tag)) return false;
    return true;
  });
}

/**
 * Splits a comma- or newline-separated tag input into a clean list: trimmed,
 * de-duplicated, empties dropped. Mirrors what the backend stores, so the
 * table does not flicker between what was typed and what came back.
 */
export function parseTagInput(raw: string): string[] {
  const out: string[] = [];
  for (const part of raw.split(/[,\n]/)) {
    const tag = part.trim();
    if (tag && !out.includes(tag)) out.push(tag);
  }
  return out;
}
