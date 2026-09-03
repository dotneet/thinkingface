import { describe, expect, it } from "vitest";
import {
  alignSeriesForKey,
  colorForRun,
  dashForRun,
  emaSmooth,
  groupByKey,
  spanGapsForMode,
} from "@/lib/chart-utils";
import type { ExpMetricSeries } from "@/types/api";

/** Matches the length of the PALETTE array in lib/chart-utils.ts. */
const PALETTE_SIZE = 20;

describe("colorForRun", () => {
  it("is stable for a given index", () => {
    expect(colorForRun(0)).toBe(colorForRun(0));
    expect(colorForRun(0)).not.toBe(colorForRun(1));
  });

  it("wraps around the palette", () => {
    expect(colorForRun(PALETTE_SIZE)).toBe(colorForRun(0));
    expect(colorForRun(2 * PALETTE_SIZE + 1)).toBe(colorForRun(1));
  });

  it("falls back to the first colour for a negative index", () => {
    // -1 % 20 is -1 in JS, which indexes off the front of the palette.
    expect(colorForRun(-1)).toBe(colorForRun(0));
  });

  it("always returns a hex colour", () => {
    for (let i = 0; i < PALETTE_SIZE + 5; i++) {
      expect(colorForRun(i)).toMatch(/^#[0-9a-f]{6}$/);
    }
  });

  it("has more colours than a typical sweep selection so 11+ runs stay distinguishable", () => {
    // Regression guard for the "10-colour palette wraps at run 11" bug: the
    // 11th run must not collide with the 1st any more.
    expect(colorForRun(10)).not.toBe(colorForRun(0));
  });
});

describe("dashForRun", () => {
  it("is solid (undefined) for every run within the first lap of the palette", () => {
    for (let i = 0; i < PALETTE_SIZE; i++) {
      expect(dashForRun(i)).toBeUndefined();
    }
  });

  it("switches dash pattern once the index wraps the palette, distinguishing a colour repeat", () => {
    // Run PALETTE_SIZE shares a colour with run 0 (colorForRun wraps), so it
    // must not also share its (solid) dash.
    expect(dashForRun(PALETTE_SIZE)).not.toBe(dashForRun(0));
    expect(dashForRun(PALETTE_SIZE)).toBeDefined();
  });

  it("is stable for a given index", () => {
    expect(dashForRun(PALETTE_SIZE)).toEqual(dashForRun(PALETTE_SIZE));
  });

  it("falls back to solid for a negative index, matching colorForRun's fallback", () => {
    expect(dashForRun(-1)).toBeUndefined();
  });
});

describe("spanGapsForMode", () => {
  it("spans gaps for line charts, so a run's line does not vanish where only ANOTHER run's own null lands", () => {
    // Regression guard: two runs sampled at different step strides (the
    // backend downsamples each run to max_points independently) fill each
    // other's slots with null in alignSeriesForKey's output. Without
    // spanGaps, uPlot draws neither a line nor a marker through an isolated
    // null (points.show is false in line mode), which erases the run
    // entirely wherever its sampling didn't line up with the others'.
    expect(spanGapsForMode("line")).toBe(true);
  });

  it("is a no-op for scatter mode, which draws no line at all", () => {
    expect(spanGapsForMode("scatter")).toBe(false);
  });
});

describe("alignSeriesForKey", () => {
  it("returns just an empty x row when there are no series", () => {
    expect(alignSeriesForKey([])).toEqual([[]]);
  });

  it("keeps a single series intact and sorts the x axis", () => {
    expect(
      alignSeriesForKey([{ run: "a", points: [[3, 30] as [number, number], [1, 10]] }]),
    ).toEqual([
      [1, 3],
      [10, 30],
    ]);
  });

  it("unions the x axes and fills gaps with null", () => {
    const rows = alignSeriesForKey([
      { run: "a", points: [[1, 10] as [number, number], [3, 30]] },
      { run: "b", points: [[2, 20] as [number, number], [3, 33]] },
    ]);
    expect(rows).toEqual([
      [1, 2, 3],
      [10, null, 30],
      [null, 20, 33],
    ]);
  });

  it("produces one all-null row for a series with no points", () => {
    expect(
      alignSeriesForKey([
        { run: "a", points: [[1, 10] as [number, number]] },
        { run: "empty", points: [] },
      ]),
    ).toEqual([[1], [10], [null]]);
  });

  it("lets a later point win when a run repeats an x value", () => {
    expect(
      alignSeriesForKey([{ run: "a", points: [[1, 10] as [number, number], [1, 99]] }]),
    ).toEqual([[1], [99]]);
  });
});

describe("emaSmooth", () => {
  it("is a no-op for alpha <= 0", () => {
    const values = [1, 2, 3];
    expect(emaSmooth(values, 0)).toBe(values);
    expect(emaSmooth(values, -1)).toBe(values);
  });

  it("seeds the average with the first value", () => {
    expect(emaSmooth([4], 0.5)).toEqual([4]);
  });

  it("blends each value with the running average", () => {
    // ema_n = alpha * ema_{n-1} + (1 - alpha) * v_n
    expect(emaSmooth([0, 10], 0.5)).toEqual([0, 5]);
    expect(emaSmooth([0, 10, 10], 0.5)).toEqual([0, 5, 7.5]);
  });

  it("passes nulls through as gaps without resetting the average", () => {
    expect(emaSmooth([0, null, 10], 0.5)).toEqual([0, null, 5]);
  });

  it("seeds from the first non-null value when the series starts with a gap", () => {
    expect(emaSmooth([null, 8], 0.5)).toEqual([null, 8]);
  });

  it("returns the input unchanged for an empty series", () => {
    expect(emaSmooth([], 0.5)).toEqual([]);
  });
});

describe("groupByKey", () => {
  const series = (run: string, key: string): ExpMetricSeries => ({
    run,
    key,
    points: [[1, 1]],
  });

  it("returns an empty map for no series", () => {
    expect(groupByKey([]).size).toBe(0);
  });

  it("collects every run that reported a metric key", () => {
    const grouped = groupByKey([series("a", "loss"), series("b", "loss"), series("a", "acc")]);
    expect([...grouped.keys()]).toEqual(["loss", "acc"]);
    expect(grouped.get("loss")?.map((s) => s.run)).toEqual(["a", "b"]);
    expect(grouped.get("acc")?.map((s) => s.run)).toEqual(["a"]);
  });

  it("preserves input order within a key", () => {
    const grouped = groupByKey([series("z", "loss"), series("a", "loss")]);
    expect(grouped.get("loss")?.map((s) => s.run)).toEqual(["z", "a"]);
  });
});
