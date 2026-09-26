import { describe, expect, it } from "vitest";

import { metricsQueryKey, metricsQueryKeyXMode } from "@/lib/experiments-query-keys";

describe("metricsQueryKey", () => {
  it("does not collide a run name containing a comma with the pair it looks like", () => {
    // The bug: `selectedNames.join(",")` made these two selections the same
    // cache key, so whichever query ran first served its series to the other.
    // Sweep names look exactly like this — `lib/experiments.ts` sends repeated
    // `run=` parameters for the same reason.
    const sweep = metricsQueryKey("acme", "bert", "sweep", ["lr=0.1,bs=32"], "step");
    const pair = metricsQueryKey("acme", "bert", "sweep", ["lr=0.1", "bs=32"], "step");
    expect(sweep).not.toEqual(pair);
  });

  it("separates an empty selection from a single empty-named run", () => {
    expect(metricsQueryKey("acme", "bert", "p", [], "step")).not.toEqual(
      metricsQueryKey("acme", "bert", "p", [""], "step"),
    );
  });

  it("is stable for the same selection, and varies with every other input", () => {
    const base = metricsQueryKey("acme", "bert", "p", ["a", "b"], "step");
    expect(metricsQueryKey("acme", "bert", "p", ["a", "b"], "step")).toEqual(base);
    expect(metricsQueryKey("acme", "bert", "p", ["a", "b"], "time")).not.toEqual(base);
    expect(metricsQueryKey("acme", "bert", "other", ["a", "b"], "step")).not.toEqual(base);
    expect(metricsQueryKey("acme", "gpt", "p", ["a", "b"], "step")).not.toEqual(base);
    expect(metricsQueryKey("other", "bert", "p", ["a", "b"], "step")).not.toEqual(base);
  });

  it("keeps the single-run page and the dashboard on one key for one run", () => {
    // run-detail asks for `[runName]`; the dashboard asks for a selection that
    // happens to hold just that run. Same request, so it must be the same
    // cache entry — the shared helper is what guarantees that.
    expect(metricsQueryKey("acme", "bert", "p", ["only"], "step")).toEqual(
      metricsQueryKey("acme", "bert", "p", ["only"], "step"),
    );
  });
});

describe("metricsQueryKeyXMode", () => {
  it("reads back the xMode segment a matching metricsQueryKey was built with", () => {
    expect(metricsQueryKeyXMode(metricsQueryKey("acme", "bert", "p", ["a"], "step"))).toBe("step");
    expect(metricsQueryKeyXMode(metricsQueryKey("acme", "bert", "p", ["a"], "time"))).toBe("time");
  });

  it("returns undefined for a key that isn't one of ours", () => {
    // Guards a placeholderData callback against a malformed/foreign key
    // rather than confidently returning the wrong segment as an xMode.
    expect(metricsQueryKeyXMode(["exp-runs", "acme", "bert", "p"])).toBeUndefined();
    expect(metricsQueryKeyXMode([])).toBeUndefined();
  });
});
