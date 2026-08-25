import { describe, expect, it } from "vitest";
import {
  hasLiveRun,
  isLiveRun,
  LIVE_REFRESH_INTERVAL_MS,
  liveRefetchInterval,
} from "@/components/experiments/live-refresh";
import type { ExpRun, RunStatus } from "@/types/api";

// The helpers under test live in components/experiments/live-refresh.ts (a
// framework-free module the experiment client components import). The suite
// lives here because vitest.config.ts only collects lib/**/*.test.ts.

function run(name: string, status: RunStatus): ExpRun {
  return {
    name,
    status,
    last_step: 0,
    num_points: 0,
    started_at: null,
    updated_at: "2026-08-25T12:00:00Z",
    config: {},
    metric_keys: [],
    summary: {},
    group: "",
    job_type: "",
    tags: [],
    archived: false,
    is_baseline: false,
    note: "",
    models: [],
  };
}

describe("isLiveRun", () => {
  it("is true only for a run that is still logging", () => {
    expect(isLiveRun(run("a", "running"))).toBe(true);
    expect(isLiveRun(run("a", "finished"))).toBe(false);
    expect(isLiveRun(run("a", "failed"))).toBe(false);
  });

  it("does not treat a stale run as live", () => {
    // The whole point of the derived status: a job killed by OOM is still
    // recorded as running, and polling for it would keep an abandoned tab
    // talking to the backend forever.
    expect(isLiveRun(run("a", "stale"))).toBe(false);
  });

  it("handles a missing run", () => {
    expect(isLiveRun(undefined)).toBe(false);
  });
});

describe("hasLiveRun", () => {
  it("is false for an empty list", () => {
    expect(hasLiveRun([])).toBe(false);
  });

  it("is true when any single run is running", () => {
    expect(hasLiveRun([run("a", "finished"), run("b", "running"), run("c", "failed")])).toBe(true);
  });

  it("is false when every run has reached a terminal or stale state", () => {
    expect(hasLiveRun([run("a", "finished"), run("b", "failed"), run("c", "stale")])).toBe(false);
  });
});

describe("liveRefetchInterval", () => {
  it("polls while a run is live", () => {
    expect(liveRefetchInterval([run("a", "running")])).toBe(LIVE_REFRESH_INTERVAL_MS);
  });

  it("returns false — no polling at all — once nothing is live", () => {
    // Not a slower interval: a finished project is a static page, and every
    // tab left open on one would otherwise poll for as long as it stayed open.
    expect(liveRefetchInterval([run("a", "finished"), run("b", "stale")])).toBe(false);
    expect(liveRefetchInterval([])).toBe(false);
  });

  it("polls no faster than the shim writes", () => {
    // The Python shim flushes every 5s; anything below that only re-reads rows
    // that cannot have changed.
    expect(LIVE_REFRESH_INTERVAL_MS).toBeGreaterThanOrEqual(5_000);
  });
});
