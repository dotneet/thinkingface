import { describe, expect, it } from "vitest";
import { type ChartData, chartDataEquals, planLogScale } from "@/lib/chart-scale";

describe("planLogScale", () => {
  it("passes everything through when log scale is off", () => {
    const data: ChartData = [
      [1, 2],
      [0, -5],
    ];
    const plan = planLogScale(data, false);
    expect(plan.data).toBe(data);
    expect(plan).toMatchObject({ logEnabled: false, hiddenPoints: 0, unavailable: false });
  });

  it("keeps the same array when every y value is positive", () => {
    const data: ChartData = [
      [1, 2, 3],
      [0.5, 1.5, 2.5],
    ];
    const plan = planLogScale(data, true);
    expect(plan.data).toBe(data);
    expect(plan).toMatchObject({ logEnabled: true, hiddenPoints: 0, unavailable: false });
  });

  // uPlot clamps a 0 or a negative value to scaleMin / 10 on a log axis: it
  // draws the point below the axis, at a position that is not the value.
  it("turns values a log axis cannot place into gaps and counts them", () => {
    const data: ChartData = [
      [1, 2, 3, 4, 5],
      [1, 0, -2, Number.NaN, 4],
    ];
    const plan = planLogScale(data, true);
    expect(plan.logEnabled).toBe(true);
    expect(plan.hiddenPoints).toBe(3);
    expect(plan.unavailable).toBe(false);
    expect(plan.data[1]).toEqual([1, null, null, null, 4]);
    // x is never masked, and the input is left alone.
    expect(plan.data[0]).toEqual([1, 2, 3, 4, 5]);
    expect(data[1]).toEqual([1, 0, -2, Number.NaN, 4]);
  });

  it("does not count an existing gap as a hidden point", () => {
    const plan = planLogScale(
      [
        [1, 2, 3],
        [1, null, 3],
      ],
      true,
    );
    expect(plan.hiddenPoints).toBe(0);
    expect(plan.logEnabled).toBe(true);
  });

  // Every value non-positive used to range the y scale from
  // [Infinity, -Infinity] — NaN limits, an empty canvas, and no error.
  it("falls back to a linear axis when nothing is positive", () => {
    const data: ChartData = [
      [1, 2, 3],
      [0, -1, -2],
      [null, 0, null],
    ];
    const plan = planLogScale(data, true);
    expect(plan.logEnabled).toBe(false);
    expect(plan.unavailable).toBe(true);
    expect(plan.hiddenPoints).toBe(0);
    expect(plan.data).toBe(data);
  });

  it("treats a chart with no series at all as unavailable rather than empty-log", () => {
    const plan = planLogScale([[1, 2, 3]], true);
    expect(plan.logEnabled).toBe(false);
    expect(plan.unavailable).toBe(true);
  });

  it("masks per series, so one all-negative run does not disable the rest", () => {
    const plan = planLogScale(
      [
        [1, 2],
        [1, 2],
        [-1, -2],
      ],
      true,
    );
    expect(plan.logEnabled).toBe(true);
    expect(plan.hiddenPoints).toBe(2);
    expect(plan.data[1]).toEqual([1, 2]);
    expect(plan.data[2]).toEqual([null, null]);
  });
});

describe("chartDataEquals", () => {
  it("reports equal for a fresh array holding the same numbers", () => {
    expect(
      chartDataEquals(
        [
          [1, 2],
          [3, null],
        ],
        [
          [1, 2],
          [3, null],
        ],
      ),
    ).toBe(true);
  });

  it("notices an appended point, a changed value, and a new series", () => {
    expect(chartDataEquals([[1, 2]], [[1, 2, 3]])).toBe(false);
    expect(chartDataEquals([[1, 2]], [[1, 5]])).toBe(false);
    expect(
      chartDataEquals(
        [[1, 2]],
        [
          [1, 2],
          [3, 4],
        ],
      ),
    ).toBe(false);
  });

  it("separates a gap from a zero", () => {
    expect(chartDataEquals([[null]], [[0]])).toBe(false);
  });

  // A metric series that reports NaN would otherwise look like it changed on
  // every 15-second poll, and every poll would drop the user's zoom.
  it("treats NaN as equal to itself", () => {
    expect(chartDataEquals([[Number.NaN]], [[Number.NaN]])).toBe(true);
  });

  it("short-circuits on identity", () => {
    const data: ChartData = [[1]];
    expect(chartDataEquals(data, data)).toBe(true);
  });
});
