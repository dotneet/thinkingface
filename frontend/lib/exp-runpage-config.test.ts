import { describe, expect, it } from "vitest";

import {
  configKeysDifferingFrom,
  entriesWithBaselineKeys,
  filterConfigEntries,
  groupSummaryKeys,
} from "@/lib/exp-runpage-config";

describe("groupSummaryKeys", () => {
  it("puts goal metrics first, system/* last, each alphabetical", () => {
    const groups = groupSummaryKeys(["train/lr", "system/gpu", "val/cer", "acc", "system/cpu"], {
      "val/cer": "min",
      acc: "max",
    });
    expect(groups).toEqual({
      goals: ["acc", "val/cer"],
      other: ["train/lr"],
      system: ["system/cpu", "system/gpu"],
    });
  });
});

describe("filterConfigEntries", () => {
  const entries = [
    { key: "lr", value: 0.0003 },
    { key: "model", value: "trocr-small" },
    { key: "optimizer", value: { name: "adamw" } },
  ];
  it("matches keys and formatted values, case-insensitively", () => {
    expect(filterConfigEntries(entries, "TROCR").map((e) => e.key)).toEqual(["model"]);
    expect(filterConfigEntries(entries, "adamw").map((e) => e.key)).toEqual(["optimizer"]);
    expect(filterConfigEntries(entries, "")).toHaveLength(3);
  });
});

describe("configKeysDifferingFrom", () => {
  it("flags changed values and keys only one side has", () => {
    const own = [
      { key: "lr", value: 0.001 },
      { key: "seed", value: 0 },
      { key: "new", value: true },
    ];
    const base = [
      { key: "lr", value: 0.0003 },
      { key: "seed", value: 0 },
      { key: "old", value: 1 },
    ];
    expect([...configKeysDifferingFrom(own, base)].sort()).toEqual(["lr", "new", "old"]);
  });

  it("compares values as displayed", () => {
    expect(configKeysDifferingFrom([{ key: "x", value: 1 }], [{ key: "x", value: 1.0 }]).size).toBe(
      0,
    );
  });
});

describe("entriesWithBaselineKeys", () => {
  it("appends baseline-only keys with no value", () => {
    expect(
      entriesWithBaselineKeys(
        [{ key: "a", value: 1 }],
        [
          { key: "a", value: 2 },
          { key: "b", value: 3 },
        ],
      ),
    ).toEqual([
      { key: "a", value: 1 },
      { key: "b", value: undefined },
    ]);
  });
});
