import { describe, expect, it } from "vitest";

import {
  bestInRuns,
  effectiveDirection,
  hasAnyGoal,
  isBestRun,
  projectRunSummaries,
  runListBest,
  runListGoals,
  summaryValue,
  toggleSortWithGoals,
} from "@/lib/exp-goals";
import type { ExpRun, RunStatus } from "@/types/api";

function run(name: string, over: Partial<ExpRun> = {}): ExpRun {
  return {
    name,
    status: "finished" as RunStatus,
    last_step: 10,
    num_points: 10,
    started_at: null,
    updated_at: "2026-09-01T00:00:00Z",
    config: {},
    metric_keys: [],
    summary: {},
    summary_min: {},
    summary_max: {},
    heartbeat_secs: 0,
    group: "",
    job_type: "",
    tags: [],
    archived: false,
    is_baseline: false,
    note: "",
    models: [],
    ...over,
  };
}

const a = run("a", {
  summary: { loss: 0.5, acc: 0.8 },
  summary_min: { loss: 0.3, acc: 0.1 },
  summary_max: { loss: 2.0, acc: 0.9 },
});

describe("runListGoals / runListBest", () => {
  it("read an older server's listing as having no goals", () => {
    expect(runListGoals({ runs: [] })).toEqual({});
    expect(runListBest(undefined)).toEqual({});
  });

  it("pass declared values through", () => {
    expect(runListGoals({ runs: [], metric_goals: { loss: "min" }, best: {} })).toEqual({
      loss: "min",
    });
  });
});

describe("effectiveDirection", () => {
  it("prefers the declared goal over the name heuristic", () => {
    expect(effectiveDirection({ loss: "max" }, "loss")).toBe("max");
  });

  it("falls back to the name heuristic", () => {
    expect(effectiveDirection({}, "val/loss")).toBe("min");
    expect(effectiveDirection({}, "acc")).toBe("max");
  });
});

describe("hasAnyGoal", () => {
  it("is false for an empty map", () => {
    expect(hasAnyGoal({})).toBe(false);
    expect(hasAnyGoal({ loss: "min" })).toBe(true);
  });
});

describe("summaryValue", () => {
  it("reads last / min / max", () => {
    expect(summaryValue(a, "loss", "last", {})).toBe(0.5);
    expect(summaryValue(a, "loss", "min", {})).toBe(0.3);
    expect(summaryValue(a, "loss", "max", {})).toBe(2.0);
  });

  it("reads best in the goal's direction", () => {
    expect(summaryValue(a, "loss", "best", { loss: "min" })).toBe(0.3);
    expect(summaryValue(a, "acc", "best", { acc: "max" })).toBe(0.9);
    expect(summaryValue(a, "acc", "best", { acc: "min" })).toBe(0.1);
  });

  it("is undefined for a metric the run lacks, or a server without min/max", () => {
    expect(summaryValue(a, "f1", "last", {})).toBeUndefined();
    const old = { ...a, summary_min: undefined } as unknown as ExpRun;
    expect(summaryValue(old, "loss", "min", {})).toBeUndefined();
  });
});

describe("projectRunSummaries", () => {
  it("returns the input itself for last", () => {
    const runs = [a];
    expect(projectRunSummaries(runs, "last", {})).toBe(runs);
  });

  it("replaces summary with the chosen values and keeps the rest", () => {
    const [projected] = projectRunSummaries([a], "min", {});
    expect(projected?.summary).toEqual({ loss: 0.3, acc: 0.1 });
    expect(projected?.name).toBe("a");
    expect(projected?.summary_max).toEqual(a.summary_max);
    // The original is not mutated.
    expect(a.summary).toEqual({ loss: 0.5, acc: 0.8 });
  });

  it("drops metrics the chosen summary does not have", () => {
    const partial = run("p", { summary: { loss: 1 }, summary_min: {} });
    expect(projectRunSummaries([partial], "min", {})[0]?.summary).toEqual({});
  });
});

describe("isBestRun", () => {
  it("matches the listing's best map", () => {
    expect(isBestRun({ loss: "a" }, "loss", "a")).toBe(true);
    expect(isBestRun({ loss: "a" }, "loss", "b")).toBe(false);
    expect(isBestRun({}, "loss", "a")).toBe(false);
  });
});

describe("bestInRuns", () => {
  const runs = [run("x", { summary: { score: 1 } }), run("y", { summary: { score: 3 } })];

  it("follows the declared goal", () => {
    expect(bestInRuns(runs, "score", { score: "min" })).toEqual({ value: 1, run: "x" });
    expect(bestInRuns(runs, "score", { score: "max" })).toEqual({ value: 3, run: "y" });
  });

  it("is null when no run has the metric", () => {
    expect(bestInRuns(runs, "loss", {})).toBeNull();
  });
});

describe("toggleSortWithGoals", () => {
  it("opens a metric sort on the goal's good end", () => {
    expect(toggleSortWithGoals(null, "metric:acc", { acc: "min" })).toEqual({
      column: "metric:acc",
      dir: "asc",
    });
    expect(toggleSortWithGoals(null, "metric:loss", { loss: "max" })).toEqual({
      column: "metric:loss",
      dir: "desc",
    });
  });

  it("flips on a second click", () => {
    expect(
      toggleSortWithGoals({ column: "metric:acc", dir: "asc" }, "metric:acc", { acc: "min" }),
    ).toEqual({ column: "metric:acc", dir: "desc" });
  });

  it("falls back to the plain toggle without a goal", () => {
    expect(toggleSortWithGoals(null, "metric:loss", {})).toEqual({
      column: "metric:loss",
      dir: "asc",
    });
    expect(toggleSortWithGoals(null, "name", { loss: "min" })).toEqual({
      column: "name",
      dir: "asc",
    });
  });
});
