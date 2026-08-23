import { describe, expect, it } from "vitest";
import {
  bestMetric,
  bestRunFor,
  buildMetricFilter,
  compareRuns,
  filterByMetric,
  groupJobTypes,
  groupRuns,
  hiddenMetricCount,
  metricColumns,
  metricDirection,
  sortGroups,
  sortRuns,
  toggleSort,
} from "@/lib/run-grouping";
import type { ExpRun } from "@/types/api";

function run(name: string, overrides: Partial<ExpRun> = {}): ExpRun {
  return {
    name,
    status: "finished",
    last_step: 10,
    num_points: 10,
    started_at: null,
    updated_at: "2026-01-01T00:00:00Z",
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
    ...overrides,
  };
}

describe("groupRuns", () => {
  it("leaves ungrouped runs flat, one row each", () => {
    const runs = [run("a"), run("b")];
    expect(groupRuns(runs)).toEqual([
      { key: "", runs: [runs[0]], grouped: false },
      { key: "", runs: [runs[1]], grouped: false },
    ]);
  });

  it("collects the members of a declared group", () => {
    const groups = groupRuns([
      run("lr-0.1", { group: "sweep" }),
      run("lr-0.2", { group: "sweep" }),
    ]);
    expect(groups).toHaveLength(1);
    expect(groups[0]?.grouped).toBe(true);
    expect(groups[0]?.runs.map((r) => r.name)).toEqual(["lr-0.1", "lr-0.2"]);
  });

  it("keeps a group where its first member was, mixed with lone runs", () => {
    const groups = groupRuns([
      run("solo-1"),
      run("lr-0.1", { group: "sweep" }),
      run("solo-2"),
      run("lr-0.2", { group: "sweep" }),
    ]);
    expect(groups.map((g) => g.key)).toEqual(["", "sweep", ""]);
    expect(groups[1]?.runs).toHaveLength(2);
  });

  it("keeps two different groups apart", () => {
    const groups = groupRuns([
      run("a", { group: "lr" }),
      run("b", { group: "seed" }),
      run("c", { group: "lr" }),
    ]);
    expect(groups.map((g) => [g.key, g.runs.length])).toEqual([
      ["lr", 2],
      ["seed", 1],
    ]);
  });
});

describe("groupJobTypes", () => {
  it("lists the declared roles once each", () => {
    expect(
      groupJobTypes([
        run("a", { job_type: "train" }),
        run("b", { job_type: "eval" }),
        run("c", { job_type: "train" }),
        run("d"),
      ]),
    ).toEqual(["eval", "train"]);
  });
});

describe("metricDirection", () => {
  it("reads loss-like names as lower-is-better", () => {
    expect(metricDirection("loss")).toBe("min");
    expect(metricDirection("val_loss")).toBe("min");
    expect(metricDirection("eval/wer")).toBe("min");
    expect(metricDirection("perplexity")).toBe("min");
  });

  it("treats everything else as higher-is-better", () => {
    expect(metricDirection("accuracy")).toBe("max");
    expect(metricDirection("f1")).toBe("max");
    // "gloss" is not a loss, and neither is "lossless".
    expect(metricDirection("glossary_hits")).toBe("max");
  });
});

describe("bestMetric", () => {
  const runs = [
    run("a", { summary: { loss: 0.5, accuracy: 0.7 } }),
    run("b", { summary: { loss: 0.2, accuracy: 0.9 } }),
    run("c", {}),
  ];

  it("takes the lowest loss and the highest accuracy", () => {
    expect(bestMetric(runs, "loss")).toBe(0.2);
    expect(bestMetric(runs, "accuracy")).toBe(0.9);
  });

  it("reports nothing when no member logged the metric", () => {
    expect(bestMetric(runs, "bleu")).toBeNull();
    expect(bestMetric([], "loss")).toBeNull();
  });

  it("names the run that holds the best value", () => {
    expect(bestRunFor(runs, "loss")?.name).toBe("b");
    expect(bestRunFor(runs, "bleu")).toBeUndefined();
  });
});

describe("metricColumns", () => {
  const runs = [
    run("a", { summary: { loss: 1, "system/cpu.percent": 12 } }),
    run("b", { summary: { accuracy: 0.5, f1: 0.4 } }),
  ];

  it("collects every non-telemetry metric, alphabetically", () => {
    expect(metricColumns(runs)).toEqual(["accuracy", "f1", "loss"]);
  });

  it("caps the columns and reports how many were left out", () => {
    expect(metricColumns(runs, 2)).toEqual(["accuracy", "f1"]);
    expect(hiddenMetricCount(runs, 2)).toBe(1);
    expect(hiddenMetricCount(runs, 5)).toBe(0);
  });
});

describe("sorting", () => {
  const a = run("a", { summary: { loss: 0.5 }, last_step: 10 });
  const b = run("b", { summary: { loss: 0.2 }, last_step: 30 });
  const c = run("c", { last_step: 20 }); // never logged a loss

  it("opens a metric header on its useful end", () => {
    expect(toggleSort(null, "metric:loss")).toEqual({ column: "metric:loss", dir: "asc" });
    expect(toggleSort(null, "metric:accuracy")).toEqual({
      column: "metric:accuracy",
      dir: "desc",
    });
    expect(toggleSort(null, "name")).toEqual({ column: "name", dir: "asc" });
  });

  it("flips the direction when the same column is clicked again", () => {
    const first = toggleSort(null, "last_step");
    expect(toggleSort(first, "last_step").dir).toBe("desc");
  });

  it("sorts by a metric, lowest first", () => {
    expect(sortRuns([a, b, c], { column: "metric:loss", dir: "asc" }).map((r) => r.name)).toEqual([
      "b",
      "a",
      "c",
    ]);
  });

  it("keeps runs without a value last in both directions", () => {
    expect(sortRuns([a, b, c], { column: "metric:loss", dir: "desc" }).map((r) => r.name)).toEqual([
      "a",
      "b",
      "c",
    ]);
    expect(compareRuns(c, a, { column: "metric:loss", dir: "desc" })).toBeGreaterThan(0);
  });

  it("sorts by a plain field too, and leaves the list alone without a sort", () => {
    expect(sortRuns([a, b, c], { column: "last_step", dir: "desc" }).map((r) => r.name)).toEqual([
      "b",
      "c",
      "a",
    ]);
    expect(sortRuns([a, b, c], null)).toEqual([a, b, c]);
  });

  it("orders groups by their own best member", () => {
    const groups = groupRuns([
      run("s1-a", { group: "slow", summary: { loss: 0.9 } }),
      run("s1-b", { group: "slow", summary: { loss: 0.8 } }),
      run("f1-a", { group: "fast", summary: { loss: 0.3 } }),
      run("f1-b", { group: "fast", summary: { loss: 0.1 } }),
    ]);
    const sorted = sortGroups(groups, { column: "metric:loss", dir: "asc" });
    expect(sorted.map((g) => g.key)).toEqual(["fast", "slow"]);
    expect(sorted[0]?.runs.map((r) => r.name)).toEqual(["f1-b", "f1-a"]);
    // The unsorted case is the input order, untouched.
    expect(sortGroups(groups, null)).toBe(groups);
  });
});

describe("metric filtering", () => {
  const runs = [
    run("a", { summary: { loss: 0.5 } }),
    run("b", { summary: { loss: 0.1 } }),
    run("c", {}),
  ];

  it("builds a filter only from a usable form state", () => {
    expect(buildMetricFilter("loss", "<", "0.3")).toEqual({ key: "loss", op: "<", value: 0.3 });
    expect(buildMetricFilter("", "<", "0.3")).toBeNull();
    expect(buildMetricFilter("loss", "~", "0.3")).toBeNull();
    expect(buildMetricFilter("loss", "<", "")).toBeNull();
    expect(buildMetricFilter("loss", "<", "abc")).toBeNull();
  });

  it("keeps the runs that answer the question", () => {
    expect(filterByMetric(runs, { key: "loss", op: "<", value: 0.3 }).map((r) => r.name)).toEqual([
      "b",
    ]);
    expect(filterByMetric(runs, { key: "loss", op: ">=", value: 0.1 }).map((r) => r.name)).toEqual([
      "a",
      "b",
    ]);
  });

  it("drops a run that never logged the metric, and filters nothing when unset", () => {
    expect(filterByMetric(runs, { key: "loss", op: ">", value: 0 }).map((r) => r.name)).toEqual([
      "a",
      "b",
    ]);
    expect(filterByMetric(runs, null)).toBe(runs);
  });
});
