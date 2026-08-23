import { describe, expect, it } from "vitest";
import {
  allTags,
  axisId,
  axisValue,
  buildConfigDiff,
  filterRuns,
  formatConfigValue,
  MISSING,
  parseTagInput,
  scatterAxes,
  scatterPoints,
} from "@/lib/run-compare";
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

describe("formatConfigValue", () => {
  it("renders scalars as they read in a config file", () => {
    expect(formatConfigValue("adam")).toBe("adam");
    expect(formatConfigValue(3)).toBe("3");
    expect(formatConfigValue(0.001)).toBe("0.001");
    expect(formatConfigValue(true)).toBe("true");
    expect(formatConfigValue(null)).toBe("null");
  });

  it("marks an absent key rather than printing undefined", () => {
    expect(formatConfigValue(undefined)).toBe(MISSING);
  });

  it("serialises nested values so they compare structurally", () => {
    expect(formatConfigValue({ b: 1, a: 2 })).toBe('{"b":1,"a":2}');
    expect(formatConfigValue([1, 2])).toBe("[1,2]");
  });
});

describe("buildConfigDiff", () => {
  it("returns no rows for runs without config", () => {
    expect(buildConfigDiff([run("a"), run("b")])).toEqual([]);
  });

  it("unions the keys of every run and sorts them", () => {
    const rows = buildConfigDiff([
      run("a", { config: { lr: 0.1, seed: 1 } }),
      run("b", { config: { batch: 32 } }),
    ]);
    expect(rows.map((r) => r.key)).toEqual(["batch", "lr", "seed"]);
  });

  it("flags a key only when the runs disagree", () => {
    const rows = buildConfigDiff([
      run("a", { config: { lr: 0.1, seed: 1 } }),
      run("b", { config: { lr: 0.2, seed: 1 } }),
    ]);
    expect(rows.find((r) => r.key === "lr")?.differs).toBe(true);
    expect(rows.find((r) => r.key === "seed")?.differs).toBe(false);
  });

  it("treats a missing key as a difference, not as equal", () => {
    const rows = buildConfigDiff([run("a", { config: { lr: 0.1 } }), run("b", { config: {} })]);
    expect(rows[0]?.values).toEqual(["0.1", MISSING]);
    expect(rows[0]?.differs).toBe(true);
  });

  it("keeps a single run's rows unflagged", () => {
    const rows = buildConfigDiff([run("a", { config: { lr: 0.1 } })]);
    expect(rows[0]?.differs).toBe(false);
  });
});

describe("scatterAxes", () => {
  it("offers numeric config keys and metric summaries", () => {
    const axes = scatterAxes([
      run("a", { config: { lr: 0.1, optimizer: "adam" }, summary: { loss: 0.5 } }),
    ]);
    expect(axes.map((a) => a.id)).toEqual([axisId("config", "lr"), axisId("metric", "loss")]);
  });

  it("skips values that cannot be plotted", () => {
    const axes = scatterAxes([
      run("a", { config: { name: "x", nested: { a: 1 }, bad: Number.NaN } }),
    ]);
    expect(axes).toEqual([]);
  });

  it("includes a key that is numeric in only one of the runs", () => {
    const axes = scatterAxes([
      run("a", { config: { lr: "auto" } }),
      run("b", { config: { lr: 0.1 } }),
    ]);
    expect(axes.map((a) => a.key)).toEqual(["lr"]);
  });
});

describe("axisValue", () => {
  const r = run("a", { config: { lr: 0.1, name: "x" }, summary: { loss: 0.5 } });

  it("reads from the requested source", () => {
    expect(axisValue(r, { id: "config:lr", label: "", source: "config", key: "lr" })).toBe(0.1);
    expect(axisValue(r, { id: "metric:loss", label: "", source: "metric", key: "loss" })).toBe(0.5);
  });

  it("is null for a non-numeric or absent value, and for no axis", () => {
    expect(
      axisValue(r, { id: "config:name", label: "", source: "config", key: "name" }),
    ).toBeNull();
    expect(
      axisValue(r, { id: "config:gone", label: "", source: "config", key: "gone" }),
    ).toBeNull();
    expect(axisValue(r, undefined)).toBeNull();
  });
});

describe("scatterPoints", () => {
  const x = { id: "config:lr", label: "", source: "config" as const, key: "lr" };
  const y = { id: "metric:loss", label: "", source: "metric" as const, key: "loss" };

  it("keeps only runs that have both coordinates", () => {
    const points = scatterPoints(
      [
        run("a", { config: { lr: 0.1 }, summary: { loss: 0.5 } }),
        run("b", { config: { lr: 0.2 } }),
        run("c", { summary: { loss: 0.3 } }),
      ],
      x,
      y,
    );
    expect(points).toEqual([{ run: "a", x: 0.1, y: 0.5, isBaseline: false }]);
  });

  it("carries the baseline flag through", () => {
    const points = scatterPoints(
      [run("a", { config: { lr: 0.1 }, summary: { loss: 0.5 }, is_baseline: true })],
      x,
      y,
    );
    expect(points[0]?.isBaseline).toBe(true);
  });

  it("is empty when an axis is not chosen", () => {
    expect(scatterPoints([run("a", { config: { lr: 1 } })], undefined, y)).toEqual([]);
  });
});

describe("allTags", () => {
  it("collects a sorted, de-duplicated set", () => {
    expect(allTags([run("a", { tags: ["b", "a"] }), run("b", { tags: ["a", "c"] })])).toEqual([
      "a",
      "b",
      "c",
    ]);
  });

  it("is empty when nothing is tagged", () => {
    expect(allTags([run("a")])).toEqual([]);
  });
});

describe("filterRuns", () => {
  const runs = [
    run("a", { tags: ["baseline"] }),
    run("b", { archived: true, tags: ["baseline"] }),
    run("c", { tags: ["sweep"] }),
  ];

  it("hides archived runs by default", () => {
    expect(filterRuns(runs, { showArchived: false }).map((r) => r.name)).toEqual(["a", "c"]);
  });

  it("includes archived runs when asked", () => {
    expect(filterRuns(runs, { showArchived: true }).map((r) => r.name)).toEqual(["a", "b", "c"]);
  });

  it("narrows to one tag, still respecting the archive filter", () => {
    expect(filterRuns(runs, { showArchived: false, tag: "baseline" }).map((r) => r.name)).toEqual([
      "a",
    ]);
    expect(filterRuns(runs, { showArchived: true, tag: "sweep" }).map((r) => r.name)).toEqual([
      "c",
    ]);
  });
});

describe("parseTagInput", () => {
  it("splits on commas and newlines, trimming as it goes", () => {
    expect(parseTagInput(" lr-sweep, seed-1 \n final ")).toEqual(["lr-sweep", "seed-1", "final"]);
  });

  it("drops empties and duplicates", () => {
    expect(parseTagInput("a,,a, ,b")).toEqual(["a", "b"]);
  });

  it("returns nothing for blank input", () => {
    expect(parseTagInput("   ")).toEqual([]);
  });
});
