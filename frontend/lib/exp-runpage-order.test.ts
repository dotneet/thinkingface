import { describe, expect, it } from "vitest";

import {
  filterRunsForPicker,
  isTypingTarget,
  projectHref,
  runNeighbours,
  runPageOrder,
  runPagePrimaryMetric,
  runPageShortcut,
} from "@/lib/exp-runpage-order";
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

const key = { altKey: false, ctrlKey: false, metaKey: false };

describe("runPagePrimaryMetric", () => {
  it("prefers a held-out goal metric some run logged", () => {
    const runs = [
      run("a", { summary_min: { "val/cer": 1, "train/loss": 2 }, summary_max: { acc: 0.9 } }),
    ];
    expect(runPagePrimaryMetric(runs, { "train/loss": "min", acc: "max", "val/cer": "min" })).toBe(
      "val/cer",
    );
  });

  it("skips a goal metric no run logged, and is undefined without goals", () => {
    const runs = [run("a", { summary_min: { loss: 1 } })];
    expect(runPagePrimaryMetric(runs, { "a/unlogged": "min", loss: "min" })).toBe("loss");
    expect(runPagePrimaryMetric(runs, {})).toBeUndefined();
  });
});

describe("runPageOrder", () => {
  it("ranks by the best value of the primary goal, keeping groups together", () => {
    const runs = [
      run("e1", { group: "E1", summary_min: { cer: 20 } }),
      run("e1b", { group: "E1", summary_min: { cer: 18 } }),
      run("e2", { group: "E2", summary_min: { cer: 15 }, summary: { cer: 30 } }),
      run("solo", { summary_min: { cer: 17 } }),
      run("none"),
    ];
    // E2 leads (15), then solo (17), then E1 led by its best member (18).
    expect(runPageOrder(runs, { cer: "min" })).toEqual(["e2", "solo", "e1b", "e1", "none"]);
  });

  it("orders a higher-is-better goal descending", () => {
    const runs = [
      run("lo", { summary_max: { acc: 0.5 } }),
      run("hi", { summary_max: { acc: 0.9 } }),
    ];
    expect(runPageOrder(runs, { acc: "max" })).toEqual(["hi", "lo"]);
  });

  it("falls back to most recently updated first without goals", () => {
    const runs = [
      run("old", { updated_at: "2026-01-01T00:00:00Z" }),
      run("new", { updated_at: "2026-03-01T00:00:00Z" }),
      run("mid", { updated_at: "2026-02-01T00:00:00Z" }),
    ];
    expect(runPageOrder(runs, {})).toEqual(["new", "mid", "old"]);
  });

  it("skips archived runs except the current one", () => {
    const runs = [run("a"), run("gone", { archived: true }), run("b")];
    expect(runPageOrder(runs, {})).not.toContain("gone");
    expect(runPageOrder(runs, {}, "gone")).toContain("gone");
  });
});

describe("runNeighbours", () => {
  it("finds both sides and stops at the ends", () => {
    expect(runNeighbours(["a", "b", "c"], "b")).toEqual({
      prev: "a",
      next: "c",
      index: 1,
      total: 3,
    });
    expect(runNeighbours(["a", "b"], "a")).toEqual({
      prev: undefined,
      next: "b",
      index: 0,
      total: 2,
    });
    expect(runNeighbours(["a"], "zz")).toEqual({ index: -1, total: 1 });
  });
});

describe("filterRunsForPicker", () => {
  const runs = [
    run("e6_all", { group: "E6" }),
    run("sweep_lr0.001", { group: "lr-sweep", tags: ["key-finding"] }),
  ];
  it("matches every term against name, group and tags", () => {
    expect(filterRunsForPicker(runs, "SWEEP key").map((r) => r.name)).toEqual(["sweep_lr0.001"]);
    expect(filterRunsForPicker(runs, "e6").map((r) => r.name)).toEqual(["e6_all"]);
    expect(filterRunsForPicker(runs, "  ")).toHaveLength(2);
    expect(filterRunsForPicker(runs, "nothing")).toEqual([]);
  });
});

describe("runPageShortcut", () => {
  it("maps brackets and j/k, and ignores modified keys", () => {
    expect(runPageShortcut({ ...key, key: "[" })).toBe("prev");
    expect(runPageShortcut({ ...key, key: "k" })).toBe("prev");
    expect(runPageShortcut({ ...key, key: "]" })).toBe("next");
    expect(runPageShortcut({ ...key, key: "j" })).toBe("next");
    expect(runPageShortcut({ ...key, key: "x" })).toBeNull();
    expect(runPageShortcut({ ...key, key: "[", metaKey: true })).toBeNull();
    expect(runPageShortcut({ ...key, key: "j", ctrlKey: true })).toBeNull();
  });
});

describe("isTypingTarget", () => {
  it("is false for a non-element", () => {
    expect(isTypingTarget(null)).toBe(false);
  });
});

describe("projectHref", () => {
  it("links the project, carrying selected runs as repeated ?runs=", () => {
    expect(projectHref("ns", "repo", "p")).toBe("/experiments/ns/repo/p");
    expect(projectHref("ns", "my repo", "p", ["a", "b", "a"])).toBe(
      "/experiments/ns/my%20repo/p?runs=a&runs=b",
    );
    expect(projectHref("ns", "r", "p", ["lr=0.1,bs=32"])).toBe(
      "/experiments/ns/r/p?runs=lr%3D0.1%2Cbs%3D32",
    );
  });
});
