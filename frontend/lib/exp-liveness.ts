import type { RunStatus } from "@/types/api";
import { RunStatusRunning, RunStatusStale } from "@/types/api";

/**
 * When a silent run turns "stale", mirrored from the backend's
 * `deriveRunStatus` (docs/dev/agent-features.md §2.6) so the run page can say
 * how long a run may go quiet before it is written off.
 *
 * The server stays the judge — the badge shows the status it derived — this
 * only explains the rule.
 */

/** A run whose client declared no heartbeat: 30 minutes, the original window. */
export const STALE_FALLBACK_SECS = 30 * 60;

/** Floor of the heartbeat-derived window, so a 5-second ping can't flicker. */
export const STALE_MIN_SECS = 2 * 60;

/** Seconds of silence after which a running run reads as stale. */
export function staleWindowSecs(heartbeatSecs: number): number {
  if (!(heartbeatSecs > 0)) return STALE_FALLBACK_SECS;
  return Math.max(4 * heartbeatSecs, STALE_MIN_SECS);
}

/** Only a run still recorded as running has a "last seen" worth showing. */
export function showsLastSeen(status: RunStatus): boolean {
  return status === RunStatusRunning || status === RunStatusStale;
}

/**
 * A duration in the largest unit that reads naturally: whole hours, else
 * minutes (to one decimal), else seconds. The caller turns the unit into
 * words through the dictionary.
 */
export function durationParts(secs: number): {
  unit: "hours" | "minutes" | "seconds";
  count: number;
} {
  if (secs >= 3600 && secs % 3600 === 0) return { unit: "hours", count: secs / 3600 };
  if (secs >= 60) return { unit: "minutes", count: Math.round((secs / 60) * 10) / 10 };
  return { unit: "seconds", count: secs };
}
