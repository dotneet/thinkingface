import { metricGoal, type MetricGoals } from "@/lib/exp-goals";
import { formatConfigValue } from "@/lib/run-compare";
import type { ConfigEntry } from "@/lib/run-config";

/**
 * Pure helpers for the run page's summary cards and config table.
 */

/** Metric keys under this prefix are machine telemetry, not results. */
const SYSTEM_PREFIX = "system/";

/** Above this many entries the config table grows a filter box. */
export const CONFIG_FILTER_THRESHOLD = 12;

export type SummaryKeyGroups = {
  /** Metrics with a declared goal — what the reader came to check. */
  goals: string[];
  /** Every other result metric. */
  other: string[];
  /** `system/*` telemetry, shown collapsed. */
  system: string[];
};

/**
 * Splits a run's summary keys by how much the reader cares: goal metrics
 * first, then the rest, then `system/*` telemetry. Each group is alphabetical.
 */
export function groupSummaryKeys(keys: Iterable<string>, goals: MetricGoals): SummaryKeyGroups {
  const out: SummaryKeyGroups = { goals: [], other: [], system: [] };
  for (const key of keys) {
    if (metricGoal(goals, key) !== undefined) out.goals.push(key);
    else if (key.startsWith(SYSTEM_PREFIX)) out.system.push(key);
    else out.other.push(key);
  }
  const byName = (a: string, b: string) => a.localeCompare(b);
  out.goals.sort(byName);
  out.other.sort(byName);
  out.system.sort(byName);
  return out;
}

/**
 * The config entries matching a filter: every whitespace-separated term must
 * appear (case-insensitively) in the key or in the formatted value.
 */
export function filterConfigEntries(entries: readonly ConfigEntry[], query: string): ConfigEntry[] {
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (terms.length === 0) return [...entries];
  return entries.filter((entry) => {
    const haystack = `${entry.key}\n${formatConfigValue(entry.value)}`.toLowerCase();
    return terms.every((term) => haystack.includes(term));
  });
}

/**
 * Keys whose value differs between a run's entries and the baseline's. A key
 * only one side has counts as different. Values are compared as the table
 * shows them, so `1` and `1.0` (both "1") are the same.
 */
export function configKeysDifferingFrom(
  entries: readonly ConfigEntry[],
  baseline: readonly ConfigEntry[],
): Set<string> {
  const base = new Map(baseline.map((entry) => [entry.key, formatConfigValue(entry.value)]));
  const out = new Set<string>();
  for (const entry of entries) {
    const other = base.get(entry.key);
    if (other === undefined || other !== formatConfigValue(entry.value)) out.add(entry.key);
  }
  const own = new Set(entries.map((entry) => entry.key));
  for (const key of base.keys()) {
    if (!own.has(key)) out.add(key);
  }
  return out;
}

/**
 * The run's entries plus a row for every key only the baseline has (value
 * `undefined`), so "the baseline set this and we did not" shows up too.
 */
export function entriesWithBaselineKeys(
  entries: readonly ConfigEntry[],
  baseline: readonly ConfigEntry[],
): ConfigEntry[] {
  const own = new Set(entries.map((entry) => entry.key));
  const extra = baseline
    .filter((entry) => !own.has(entry.key))
    .map((entry) => ({ key: entry.key, value: undefined }));
  return [...entries, ...extra];
}
