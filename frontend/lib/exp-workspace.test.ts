import { describe, expect, it } from "vitest";

import {
  bestOf,
  compareForSidebar,
  countByStatus,
  defaultRunSelection,
  defaultSidebarSort,
  MAX_URL_RUNS_CHARS,
  orderChartKeys,
  orderedGoalMetrics,
  parseRunsParam,
  parseSidebarSort,
  parseView,
  primaryGoalMetric,
  rankRunsByMetric,
  runMatchesQuery,
  sidebarGroups,
  sidebarSortValue,
  workspaceSearch,
} from "@/lib/exp-workspace";
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

/** A run whose CER bottomed out at `cer`. */
function cer(name: string, value: number, over: Partial<ExpRun> = {}): ExpRun {
  return run(name, {
    summary: { "val/cer": value + 0.01 },
    summary_min: { "val/cer": value },
    summary_max: { "val/cer": value + 0.1 },
    ...over,
  });
}

describe("parseView", () => {
  it("accepts known views and falls back to charts", () => {
    expect(parseView("table")).toBe("table");
    expect(parseView(["notes", "table"])).toBe("notes");
    expect(parseView(undefined)).toBe("charts");
    expect(parseView("bogus")).toBe("charts");
  });
});

describe("orderedGoalMetrics / primaryGoalMetric", () => {
  const runs = [
    run("a", {
      summary: { "train/loss": 1, "val/cer": 0.1, "system/gpu": 3, "test/acc": 0.9 },
    }),
  ];

  it("puts held-out metrics first, drops system and unlogged goals", () => {
    const goals = {
      "train/loss": "min",
      "val/cer": "min",
      "system/gpu": "min",
      "test/acc": "max",
      "gone/metric": "min",
    } as const;
    expect(orderedGoalMetrics(goals, runs)).toEqual(["test/acc", "val/cer", "train/loss"]);
    expect(primaryGoalMetric(goals, runs)).toBe("test/acc");
  });

  it("matches the segment, not a substring", () => {
    // "interval" contains "val" but is not a validation metric.
    const goals = { interval: "min", "cvl_val/CER": "min" } as const;
    const rs = [run("a", { summary: { interval: 1, "cvl_val/CER": 1 } })];
    expect(primaryGoalMetric(goals, rs)).toBe("cvl_val/CER");
  });

  it("is undefined without goals", () => {
    expect(primaryGoalMetric({}, runs)).toBeUndefined();
  });
});

describe("rankRunsByMetric / bestOf", () => {
  it("ranks by the best value in the goal direction, dropping runs without it", () => {
    const goals = { "val/cer": "min" } as const;
    const runs = [cer("b", 0.3), cer("a", 0.1), run("none"), cer("c", 0.2)];
    expect(rankRunsByMetric(runs, "val/cer", goals).map((r) => r.name)).toEqual(["a", "c", "b"]);
    expect(bestOf(runs, "val/cer", goals)).toEqual({ run: "a", value: 0.1 });
  });

  it("uses summary_max for a metric to maximise", () => {
    const goals = { "val/cer": "max" } as const;
    const runs = [cer("a", 0.1), cer("b", 0.3)];
    expect(rankRunsByMetric(runs, "val/cer", goals).map((r) => r.name)).toEqual(["b", "a"]);
  });

  it("is null when nobody logged the metric", () => {
    expect(bestOf([run("a")], "val/cer", {})).toBeNull();
  });
});

describe("defaultRunSelection", () => {
  const goals = { "val/cer": "min" } as const;

  it("takes running runs, then the best runs, up to six", () => {
    const runs = [
      cer("r1", 0.9, { status: "running", updated_at: "2026-09-02T00:00:00Z" }),
      cer("b1", 0.1),
      cer("b2", 0.2),
      cer("b3", 0.3),
      cer("b4", 0.4),
      cer("b5", 0.5),
      cer("b6", 0.6),
    ];
    expect(defaultRunSelection(runs, goals)).toEqual(["r1", "b1", "b2", "b3", "b4", "b5"]);
  });

  it("caps running runs at three when a goal needs room", () => {
    const running = Array.from({ length: 5 }, (_, i) =>
      cer(`r${i}`, 0.9, { status: "running", updated_at: `2026-09-0${i + 1}T00:00:00Z` }),
    );
    const runs = [...running, cer("best", 0.01), cer("second", 0.02), cer("third", 0.03)];
    const picked = defaultRunSelection(runs, goals);
    expect(picked).toHaveLength(6);
    // The three most recently heard-from running runs, then the best ones.
    expect(picked).toEqual(expect.arrayContaining(["r4", "r3", "r2", "best"]));
    expect(picked).not.toContain("r0");
  });

  it("never picks archived runs", () => {
    const runs = [cer("arch", 0.01, { archived: true }), cer("a", 0.2)];
    expect(defaultRunSelection(runs, goals)).toEqual(["a"]);
  });

  it("falls back to the five most recently updated without a goal", () => {
    const runs = Array.from({ length: 8 }, (_, i) =>
      run(`r${i}`, { updated_at: `2026-09-0${i + 1}T00:00:00Z` }),
    );
    expect(defaultRunSelection(runs, {})).toEqual(["r3", "r4", "r5", "r6", "r7"]);
  });

  it("keeps running runs and tops up with recent ones without a goal", () => {
    const runs = [
      run("old", { updated_at: "2026-08-01T00:00:00Z" }),
      run("live", { status: "running", updated_at: "2026-07-01T00:00:00Z" }),
      run("new", { updated_at: "2026-09-01T00:00:00Z" }),
    ];
    expect(defaultRunSelection(runs, {})).toEqual(["old", "live", "new"]);
  });

  it("returns names in project order", () => {
    const runs = [cer("z", 0.3), cer("a", 0.1)];
    expect(defaultRunSelection(runs, goals)).toEqual(["z", "a"]);
  });
});

describe("runMatchesQuery", () => {
  const r = run("e6_all_oom", { group: "E6", tags: ["dead-end"], job_type: "train" });

  it("matches name, group, tags and job type case-insensitively", () => {
    expect(runMatchesQuery(r, "OOM")).toBe(true);
    expect(runMatchesQuery(r, "e6")).toBe(true);
    expect(runMatchesQuery(r, "dead")).toBe(true);
    expect(runMatchesQuery(r, "train")).toBe(true);
    expect(runMatchesQuery(r, "sweep")).toBe(false);
  });

  it("requires every term", () => {
    expect(runMatchesQuery(r, "e6 oom")).toBe(true);
    expect(runMatchesQuery(r, "e6 sweep")).toBe(false);
    expect(runMatchesQuery(r, "   ")).toBe(true);
  });
});

describe("countByStatus", () => {
  it("counts each status and the total", () => {
    const counts = countByStatus([
      run("a", { status: "running" }),
      run("b", { status: "running" }),
      run("c", { status: "failed" }),
      run("d"),
    ]);
    expect(counts).toEqual({ all: 4, running: 2, stale: 0, finished: 1, failed: 1 });
  });
});

describe("sidebar sort", () => {
  const goals = { "val/cer": "min" } as const;

  it("round-trips through the select value", () => {
    for (const sort of [
      { kind: "metric", metric: "a:b/c" },
      { kind: "updated" },
      { kind: "name" },
    ] as const) {
      expect(parseSidebarSort(sidebarSortValue(sort))).toEqual(sort);
    }
    expect(parseSidebarSort("junk")).toEqual({ kind: "updated" });
  });

  it("defaults to the primary metric, else newest", () => {
    expect(defaultSidebarSort("val/cer")).toEqual({ kind: "metric", metric: "val/cer" });
    expect(defaultSidebarSort(undefined)).toEqual({ kind: "updated" });
  });

  it("sorts a run without the metric last", () => {
    const sort = { kind: "metric", metric: "val/cer" } as const;
    expect(compareForSidebar(run("x"), cer("y", 0.5), sort, goals)).toBeGreaterThan(0);
    expect(compareForSidebar(cer("y", 0.5), run("x"), sort, goals)).toBeLessThan(0);
  });

  it("orders groups by their best member and members inside them", () => {
    const runs = [
      cer("g1-a", 0.5, { group: "g1" }),
      cer("solo", 0.3),
      cer("g2-a", 0.4, { group: "g2" }),
      cer("g2-b", 0.1, { group: "g2" }),
      cer("g1-b", 0.2, { group: "g1" }),
    ];
    const groups = sidebarGroups(runs, { kind: "metric", metric: "val/cer" }, goals);
    expect(groups.map((g) => g.key || g.runs[0]?.name)).toEqual(["g2", "g1", "solo"]);
    expect(groups[0]?.runs.map((r) => r.name)).toEqual(["g2-b", "g2-a"]);
  });

  it("orders groups by name when sorting by name", () => {
    const runs = [run("m", { group: "zeta" }), run("b"), run("a", { group: "alpha" })];
    const groups = sidebarGroups(runs, { kind: "name" }, {});
    expect(groups.map((g) => g.key || g.runs[0]?.name)).toEqual(["alpha", "b", "zeta"]);
  });
});

describe("orderChartKeys", () => {
  it("puts goal metrics first, then alphabetical, system split off", () => {
    expect(
      orderChartKeys(
        ["train/lr", "system/gpu", "val/wer", "train/loss", "val/cer", "system/cpu"],
        ["val/cer", "val/wer"],
      ),
    ).toEqual({
      main: ["val/cer", "val/wer", "train/loss", "train/lr"],
      system: ["system/cpu", "system/gpu"],
    });
  });
});

describe("URL state", () => {
  it("parses absent, empty and repeated runs", () => {
    expect(parseRunsParam(undefined)).toBeNull();
    expect(parseRunsParam("")).toEqual([]);
    expect(parseRunsParam(["a", "lr=0.1,bs=32", "a"])).toEqual(["a", "lr=0.1,bs=32"]);
  });

  it("writes view and runs in project order, keeping other params", () => {
    const search = workspaceSearch(
      "?foo=1",
      "table",
      ["b", "lr=0.1,bs=32"],
      ["lr=0.1,bs=32", "a", "b"],
    );
    const params = new URLSearchParams(search);
    expect(params.get("foo")).toBe("1");
    expect(params.get("view")).toBe("table");
    expect(params.getAll("runs")).toEqual(["lr=0.1,bs=32", "b"]);
  });

  it("omits the default view and leaves runs alone when untouched", () => {
    expect(workspaceSearch("?runs=a&view=table", "charts", null, ["a"])).toBe("?runs=a");
  });

  it("writes an explicit empty selection", () => {
    expect(
      parseRunsParam(new URLSearchParams(workspaceSearch("", "charts", [], ["a"])).getAll("runs")),
    ).toEqual([]);
  });

  it("drops the selection from the URL when it is too long", () => {
    const names = Array.from({ length: 200 }, (_, i) => `run-with-a-long-name-${i}`);
    expect(names.join("").length).toBeGreaterThan(MAX_URL_RUNS_CHARS);
    const search = workspaceSearch("?runs=old", "scatter", names, names);
    expect(new URLSearchParams(search).getAll("runs")).toEqual([]);
    expect(new URLSearchParams(search).get("view")).toBe("scatter");
  });
});
