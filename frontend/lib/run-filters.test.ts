import { describe, expect, it } from "vitest";
import { dropGoneRunFilters, type RunFilters } from "@/hooks/use-run-filters";

const base: RunFilters = {
  showArchived: false,
  tag: "",
  metric: "",
  op: "<",
  value: "0.1",
};

describe("dropGoneRunFilters", () => {
  it("keeps a tag and metric that are still on offer", () => {
    const filters = { ...base, tag: "lr-sweep", metric: "loss" };
    expect(dropGoneRunFilters(filters, ["lr-sweep", "seed"], ["acc", "loss"])).toEqual(filters);
  });

  it("drops a tag the project no longer has", () => {
    const filters = { ...base, tag: "lr-sweep", metric: "loss" };
    expect(dropGoneRunFilters(filters, ["seed"], ["loss"])).toEqual({
      ...filters,
      tag: "",
    });
  });

  it("drops a metric the project no longer logs", () => {
    const filters = { ...base, tag: "seed", metric: "loss" };
    expect(dropGoneRunFilters(filters, ["seed"], ["acc"])).toEqual({
      ...filters,
      metric: "",
    });
  });

  it("drops both when the option lists are empty, so a ghost filter cannot hide every run after the pickers unmount", () => {
    const filters = { ...base, tag: "lr-sweep", metric: "loss", value: "0.3" };
    expect(dropGoneRunFilters(filters, [], [])).toEqual({
      ...filters,
      tag: "",
      metric: "",
    });
  });

  it("leaves an unset filter alone", () => {
    expect(dropGoneRunFilters(base, ["lr-sweep"], ["loss"])).toEqual(base);
  });
});
