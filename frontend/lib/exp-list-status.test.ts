import { describe, expect, it } from "vitest";

import { previewList, statusCountList } from "@/lib/exp-list-status";

describe("statusCountList", () => {
  it("lists non-zero counts attention-first, finished only on request", () => {
    const counts = { finished: 22, failed: 1, running: 2, stale: 0 };
    expect(statusCountList(counts)).toEqual([
      { status: "running", count: 2 },
      { status: "failed", count: 1 },
    ]);
    expect(statusCountList(counts, true).at(-1)).toEqual({ status: "finished", count: 22 });
  });

  it("treats a missing map as empty", () => {
    expect(statusCountList(undefined)).toEqual([]);
    expect(statusCountList(null, true)).toEqual([]);
  });
});

describe("previewList", () => {
  it("returns the first items and how many were hidden", () => {
    expect(previewList(["a", "b", "c", "d"], 3)).toEqual({ shown: ["a", "b", "c"], hidden: 1 });
    expect(previewList(["a"], 3)).toEqual({ shown: ["a"], hidden: 0 });
    expect(previewList(["a", "b"], 0)).toEqual({ shown: ["a"], hidden: 1 });
  });
});
