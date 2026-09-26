import { describe, expect, it } from "vitest";

import {
  axisSizeFor,
  formatAxisTicks,
  formatColumnValue,
  formatTickValue,
  paddedRange,
} from "@/lib/exp-chart-ticks";

describe("formatAxisTicks", () => {
  it("gives a learning-rate axis readable, distinct labels", () => {
    // What uPlot's own formatter printed as "0 0 0 0".
    expect(formatAxisTicks([0, 0.0001, 0.0002, 0.0003])).toEqual(["0", "1e-4", "2e-4", "3e-4"]);
    expect(formatAxisTicks([0.00025, 0.0003, 0.00035])).toEqual(["2.5e-4", "3.0e-4", "3.5e-4"]);
  });

  it("uses the fewest decimals that keep ticks exact", () => {
    expect(formatAxisTicks([0, 0.5, 1, 1.5])).toEqual(["0.0", "0.5", "1.0", "1.5"]);
    expect(formatAxisTicks([0.02, 0.04, 0.06])).toEqual(["0.02", "0.04", "0.06"]);
    expect(formatAxisTicks([0, 200, 400])).toEqual(["0", "200", "400"]);
  });

  it("uses an SI suffix for large magnitudes", () => {
    expect(formatAxisTicks([0, 5000, 10000, 15000])).toEqual(["0", "5k", "10k", "15k"]);
    expect(formatAxisTicks([10000, 12500, 15000])).toEqual(["10.0k", "12.5k", "15.0k"]);
    expect(formatAxisTicks([2e6, 4e6])).toEqual(["2M", "4M"]);
  });

  it("keeps thousands separators below the SI threshold", () => {
    expect(formatAxisTicks([1000, 2000, 3000])).toEqual(["1,000", "2,000", "3,000"]);
  });

  it("handles negatives, nulls and empty input", () => {
    expect(formatAxisTicks([-1, 0, 1])).toEqual(["-1", "0", "1"]);
    expect(formatAxisTicks([null, 1, 2])).toEqual(["", "1", "2"]);
    expect(formatAxisTicks([])).toEqual([]);
    expect(formatAxisTicks([Number.NaN])).toEqual([""]);
  });

  it("formats each log tick on its own", () => {
    expect(formatAxisTicks([0.0001, 0.001, 0.01, 0.1, 1, 10], true)).toEqual([
      "1e-4",
      "0.001",
      "0.01",
      "0.1",
      "1",
      "10",
    ]);
  });
});

describe("formatTickValue", () => {
  it("covers every magnitude", () => {
    expect(formatTickValue(0)).toBe("0");
    expect(formatTickValue(0.00031)).toBe("3.1e-4");
    expect(formatTickValue(0.1234567)).toBe("0.1235");
    expect(formatTickValue(123456)).toBe("123k");
  });
});

describe("axisSizeFor", () => {
  it("grows with the longest label and has a floor", () => {
    expect(axisSizeFor(["1"])).toBe(36);
    expect(axisSizeFor(["12,345.5"])).toBeGreaterThan(axisSizeFor(["0.1"]));
    expect(axisSizeFor(null)).toBe(36);
  });
});

describe("paddedRange", () => {
  it("pads around the data instead of anchoring at zero", () => {
    const [min, max] = paddedRange(0.04, 0.09);
    expect(min).toBeGreaterThan(0.03);
    expect(min).toBeLessThan(0.04);
    expect(max).toBeGreaterThan(0.09);
  });

  it("opens a single value into a range", () => {
    expect(paddedRange(5, 5)).toEqual([4.5, 5.5]);
    expect(paddedRange(0, 0)).toEqual([-1, 1]);
  });

  it("passes nulls through", () => {
    expect(paddedRange(null, 1)).toEqual([null, 1]);
  });
});

describe("formatColumnValue", () => {
  it("keeps four significant digits with trailing zeros", () => {
    expect(formatColumnValue(17.6)).toBe("17.60");
    expect(formatColumnValue(14.1349)).toBe("14.13");
    expect(formatColumnValue(0.04123)).toBe("0.04123");
    expect(formatColumnValue(0.0003)).toBe("3e-4");
    expect(formatColumnValue(123456)).toBe("123k");
    expect(formatColumnValue(0)).toBe("0");
  });
});
