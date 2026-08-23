import { describe, expect, it } from "vitest";
import { alignSeriesForKey, colorForRun, emaSmooth, groupByKey } from "@/lib/chart-utils";
import type { ExpMetricSeries } from "@/types/api";

describe("colorForRun", () => {
  it("is stable for a given index", () => {
    expect(colorForRun(0)).toBe(colorForRun(0));
    expect(colorForRun(0)).not.toBe(colorForRun(1));
  });

  it("wraps around the palette", () => {
    expect(colorForRun(10)).toBe(colorForRun(0));
    expect(colorForRun(21)).toBe(colorForRun(1));
  });

  it("falls back to the first colour for a negative index", () => {
    // -1 % 10 is -1 in JS, which indexes off the front of the palette.
    expect(colorForRun(-1)).toBe(colorForRun(0));
  });

  it("always returns a hex colour", () => {
    for (let i = 0; i < 12; i++) {
      expect(colorForRun(i)).toMatch(/^#[0-9a-f]{6}$/);
    }
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
