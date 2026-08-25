import type { ExpRun } from "@/types/api";
import { RunStatusRunning } from "@/types/api";

/**
 * When an experiment page re-reads itself while a run is still training.
 *
 * Watching a loss curve move is the whole reason this section exists, and
 * until now nothing on these pages updated without a manual reload: a run
 * opened mid-training showed the numbers it had when the page was rendered,
 * forever.
 *
 * 15 seconds. The producing side sets the floor — the Python shim flushes its
 * buffer every 5 seconds (`_FLUSH_INTERVAL_SECONDS` in
 * clients/python/thinkingface/trackio/__init__.py) — so polling faster than
 * that only re-reads the same rows, and polling three times slower still lands
 * within a few flushes of the truth while costing a third of the requests.
 * Above roughly half a minute the charts stop reading as live and start
 * reading as stuck, which is the failure this is here to fix.
 */
export const LIVE_REFRESH_INTERVAL_MS = 15_000;

/**
 * True when this run is still producing data.
 *
 * Only "running" counts. "stale" is the server's derived verdict that a run
 * recorded as running has not been heard from in a long time (see
 * `runStaleAfter` in backend/internal/api/experiments.go) — almost always a
 * job that was killed without getting to call finish(). Polling for it would
 * keep a dead experiment's page hitting the backend for as long as somebody
 * leaves the tab open, which is exactly what this must not do.
 */
export function isLiveRun(run: ExpRun | undefined): boolean {
  return run?.status === RunStatusRunning;
}

/** True when at least one of these runs is still producing data. */
export function hasLiveRun(runs: readonly ExpRun[]): boolean {
  return runs.some((run) => isLiveRun(run));
}

/**
 * The `refetchInterval` for a query whose data comes from `runs`: the poll
 * interval while any of them is live, and `false` — no polling at all — the
 * moment none is.
 *
 * `false` rather than a long interval on purpose. A project whose runs all
 * finished last week is a static page; leaving it on a slow timer would still
 * mean every abandoned tab in the building talking to the backend forever.
 */
export function liveRefetchInterval(runs: readonly ExpRun[]): number | false {
  return hasLiveRun(runs) ? LIVE_REFRESH_INTERVAL_MS : false;
}
