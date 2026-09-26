import { describe, expect, it } from "vitest";

import { durationParts, showsLastSeen, staleWindowSecs } from "@/lib/exp-liveness";
import type { RunStatus } from "@/types/api";

describe("staleWindowSecs", () => {
  it("falls back to 30 minutes without a heartbeat", () => {
    expect(staleWindowSecs(0)).toBe(1800);
  });

  it("is four heartbeats", () => {
    expect(staleWindowSecs(60)).toBe(240);
  });

  it("never drops below two minutes", () => {
    expect(staleWindowSecs(5)).toBe(120);
  });
});

describe("showsLastSeen", () => {
  it("is shown for running and stale runs only", () => {
    expect(showsLastSeen("running" as RunStatus)).toBe(true);
    expect(showsLastSeen("stale" as RunStatus)).toBe(true);
    expect(showsLastSeen("finished" as RunStatus)).toBe(false);
    expect(showsLastSeen("failed" as RunStatus)).toBe(false);
  });
});

describe("durationParts", () => {
  it("uses seconds below a minute", () => {
    expect(durationParts(45)).toEqual({ unit: "seconds", count: 45 });
  });

  it("uses minutes, to one decimal", () => {
    expect(durationParts(120)).toEqual({ unit: "minutes", count: 2 });
    expect(durationParts(200)).toEqual({ unit: "minutes", count: 3.3 });
  });

  it("uses whole hours", () => {
    expect(durationParts(7200)).toEqual({ unit: "hours", count: 2 });
  });
});
