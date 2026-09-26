import type { RunStatus } from "@/types/api";
import { RunStatusFailed, RunStatusFinished, RunStatusRunning, RunStatusStale } from "@/types/api";

/**
 * Pure helpers for the experiment listings (`/experiments` and a repository's
 * project list). Framework-free so a Server Component can import them.
 */

/**
 * The order a project's status counts read in: what needs attention first.
 * Finished is last and is left out of the attention line by default — "22
 * finished" says nothing a reader acts on.
 */
export const STATUS_ORDER: readonly RunStatus[] = [
  RunStatusRunning,
  RunStatusStale,
  RunStatusFailed,
  RunStatusFinished,
];

export type StatusCount = { status: RunStatus; count: number };

/**
 * The non-zero counts of a project's `status_counts`, in {@link STATUS_ORDER}.
 * `includeFinished` adds the finished count at the end.
 */
export function statusCountList(
  counts: Partial<Record<RunStatus, number>> | null | undefined,
  includeFinished = false,
): StatusCount[] {
  const out: StatusCount[] = [];
  for (const status of STATUS_ORDER) {
    if (status === RunStatusFinished && !includeFinished) continue;
    const count = counts?.[status];
    if (typeof count === "number" && count > 0) out.push({ status, count });
  }
  return out;
}

/**
 * The first `max` of a list plus how many were left out, for a "a, b, c +4"
 * line. `max` below 1 is treated as 1 so a non-empty list always shows
 * something.
 */
export function previewList<T>(items: readonly T[], max: number): { shown: T[]; hidden: number } {
  const limit = Math.max(1, Math.floor(max));
  return { shown: items.slice(0, limit), hidden: Math.max(0, items.length - limit) };
}
