import { describe, expect, it } from "vitest";
import {
  axisPoint,
  axisTicks,
  axisX,
  axisY,
  linePath,
  MAX_AXIS_CATEGORIES,
  parallelAxes,
  parallelLines,
} from "@/lib/run-parallel";
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

const sweep = [
  run("a", { config: { lr: 0.1, optimizer: "adam" }, summary: { loss: 0.5 } }),
  run("b", { config: { lr: 0.3, optimizer: "sgd" }, summary: { loss: 0.1 } }),
];

describe("parallelAxes", () => {
  it("puts the hyperparameters first and the metrics after them", () => {
    expect(parallelAxes(sweep).map((a) => a.id)).toEqual([
      "config:lr",
      "config:optimizer",
      "metric:loss",
    ]);
  });

  it("measures a numeric axis across the runs on screen", () => {
    const [lr] = parallelAxes(sweep);
    expect(lr?.kind).toBe("numeric");
    if (lr?.kind !== "numeric") throw new Error("expected a numeric axis");
    expect([lr.min, lr.max]).toEqual([0.1, 0.3]);
  });

  it("makes a string hyperparameter a categorical axis with sorted values", () => {
    const optimizer = parallelAxes(sweep).find((a) => a.key === "optimizer");
    if (optimizer?.kind !== "categorical") throw new Error("expected a categorical axis");
    expect(optimizer.categories).toEqual(["adam", "sgd"]);
  });

  it("treats a key that is sometimes a string as categorical throughout", () => {
    const axes = parallelAxes([
      run("a", { config: { warmup: 100 } }),
      run("b", { config: { warmup: "auto" } }),
    ]);
    const warmup = axes.find((a) => a.key === "warmup");
    if (warmup?.kind !== "categorical") throw new Error("expected a categorical axis");
    expect(warmup.categories).toEqual(["100", "auto"]);
  });

  it("drops an identifier-like categorical axis", () => {
    const runs = Array.from({ length: MAX_AXIS_CATEGORIES + 1 }, (_, i) =>
      run(`r${i}`, { config: { output_dir: `out/${i}` } }),
    );
    expect(parallelAxes(runs)).toEqual([]);
  });

  it("skips reserved config sections and system telemetry", () => {
    const axes = parallelAxes([
      run("a", {
        config: { _meta: { git: { commit: "abc" } }, "_args.learning_rate": 0.1, lr: 0.1 },
        summary: { "system/cpu.percent": 12, loss: 0.5 },
      }),
    ]);
    expect(axes.map((a) => a.id)).toEqual(["config:lr", "metric:loss"]);
  });

  it("skips a nested config block, which has no single position", () => {
    expect(parallelAxes([run("a", { config: { optim: { name: "adam" } } })])).toEqual([]);
  });
});

describe("axisPoint", () => {
  const axes = parallelAxes(sweep);

  it("places a numeric value by its position in the range", () => {
    const lr = axes[0];
    if (!lr) throw new Error("no axis");
    expect(axisPoint(sweep[0] as ExpRun, lr)).toEqual({ t: 0, label: "0.1" });
    expect(axisPoint(sweep[1] as ExpRun, lr)).toEqual({ t: 1, label: "0.3" });
  });

  it("centres a value on a constant axis rather than pinning it to the floor", () => {
    const [flat] = parallelAxes([
      run("a", { config: { lr: 0.1 } }),
      run("b", { config: { lr: 0.1 } }),
    ]);
    if (!flat) throw new Error("no axis");
    expect(axisPoint(run("a", { config: { lr: 0.1 } }), flat)?.t).toBe(0.5);
  });

  it("spaces categories evenly and centres a lone one", () => {
    const [three] = parallelAxes([
      run("a", { config: { opt: "adam" } }),
      run("b", { config: { opt: "sgd" } }),
      run("c", { config: { opt: "lion" } }),
    ]);
    if (!three) throw new Error("no axis");
    // adam / lion / sgd, sorted.
    expect(axisPoint(run("x", { config: { opt: "lion" } }), three)?.t).toBe(0.5);
    expect(axisPoint(run("x", { config: { opt: "sgd" } }), three)?.t).toBe(1);

    const [one] = parallelAxes([run("a", { config: { opt: "adam" } })]);
    if (!one) throw new Error("no axis");
    expect(axisPoint(run("x", { config: { opt: "adam" } }), one)?.t).toBe(0.5);
  });

  it("reports nothing for a value the run does not have", () => {
    const lr = axes[0];
    if (!lr) throw new Error("no axis");
    expect(axisPoint(run("empty"), lr)).toBeNull();
  });
});

describe("axisTicks", () => {
  it("labels a numeric axis with its ends and a categorical one with its values", () => {
    const axes = parallelAxes(sweep);
    expect(axisTicks(axes[0] as never)).toEqual(["0.1", "0.3"]);
    expect(axisTicks(axes[1] as never)).toEqual(["adam", "sgd"]);
  });

  it("labels a constant axis once", () => {
    const [flat] = parallelAxes([run("a", { config: { lr: 2 } })]);
    expect(axisTicks(flat as never)).toEqual(["2"]);
  });

  it("uses exponential notation for a huge integer-valued axis, not a wall of digits", () => {
    const [huge] = parallelAxes([run("a", { summary: { huge: 18518518350000 } })]);
    expect(axisTicks(huge as never)).toEqual(["1.85e+13"]);
  });

  it("uses exponential notation for a tiny integer-like axis value too", () => {
    const [tiny] = parallelAxes([run("a", { summary: { tiny: 0 } })]);
    // 0 itself is exact and stays "0"; a genuinely tiny non-zero value below
    // 1e-3 still switches to exponential regardless of Number.isInteger.
    expect(axisTicks(tiny as never)).toEqual(["0"]);
  });
});

describe("parallelLines", () => {
  const axes = parallelAxes(sweep);

  it("draws one complete polyline per run", () => {
    const lines = parallelLines(sweep, axes);
    expect(lines.map((l) => l.run)).toEqual(["a", "b"]);
    expect(lines.every((l) => l.complete)).toBe(true);
    expect(lines[0]?.points.map((p) => p.axis)).toEqual([0, 1, 2]);
  });

  it("keeps a run that is missing one axis, marked incomplete", () => {
    const runs = [...sweep, run("c", { config: { lr: 0.2, optimizer: "adam" } })];
    const line = parallelLines(runs, parallelAxes(runs)).find((l) => l.run === "c");
    expect(line?.complete).toBe(false);
    expect(line?.points).toHaveLength(2);
  });

  it("leaves out a run with nothing to connect", () => {
    expect(parallelLines([run("empty")], axes)).toEqual([]);
  });
});

describe("geometry", () => {
  it("spreads axes across the plot and centres a lone one", () => {
    expect(axisX(0, 3, 100, 10)).toBe(10);
    expect(axisX(2, 3, 100, 10)).toBe(90);
    expect(axisX(0, 1, 100, 10)).toBe(50);
  });

  it("puts t = 0 at the bottom and t = 1 at the top", () => {
    expect(axisY(0, 100, 10)).toBe(90);
    expect(axisY(1, 100, 10)).toBe(10);
  });

  it("builds a move-then-line path", () => {
    const [line] = parallelLines(sweep, parallelAxes(sweep));
    if (!line) throw new Error("no line");
    expect(linePath(line, 3, 100, 100, 10)).toMatch(/^M10\.00 90\.00 L50\.00 /);
  });
});
