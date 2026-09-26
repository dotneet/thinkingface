import { describe, expect, it } from "vitest";

import { parseOffset } from "@/lib/pagination";

describe("parseOffset", () => {
  it("parses an ordinary offset", () => {
    expect(parseOffset("30")).toBe(30);
    expect(parseOffset("0")).toBe(0);
  });

  it("defaults to 0 when absent", () => {
    expect(parseOffset(undefined)).toBe(0);
    expect(parseOffset(null)).toBe(0);
    expect(parseOffset("")).toBe(0);
  });

  it("rejects a negative offset instead of passing it through", () => {
    // The bug this exists to fix: `Number("-5") || 0` is `-5`, not `0`,
    // because a nonzero number is truthy.
    expect(parseOffset("-5")).toBe(0);
    expect(parseOffset("-1")).toBe(0);
  });

  it("rejects a fractional offset instead of passing it through", () => {
    expect(parseOffset("2.5")).toBe(0);
  });

  it("rejects non-numeric garbage", () => {
    expect(parseOffset("abc")).toBe(0);
    expect(parseOffset("30abc")).toBe(0);
  });

  it("rejects Infinity", () => {
    expect(parseOffset("Infinity")).toBe(0);
    expect(parseOffset("-Infinity")).toBe(0);
  });
});
