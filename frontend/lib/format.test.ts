import { afterEach, describe, expect, it, vi } from "vitest";
import {
  formatBytes,
  formatCompactNumber,
  formatDate,
  formatDateTime,
  formatNumber,
  formatRelativeTime,
} from "@/lib/format";

describe("formatBytes", () => {
  it("returns a placeholder for non-finite or negative input", () => {
    expect(formatBytes(Number.NaN)).toBe("-");
    expect(formatBytes(Number.POSITIVE_INFINITY)).toBe("-");
    expect(formatBytes(-1)).toBe("-");
  });

  it("special-cases zero", () => {
    expect(formatBytes(0)).toBe("0 B");
  });

  it("keeps whole bytes below the first unit boundary", () => {
    expect(formatBytes(1)).toBe("1 B");
    expect(formatBytes(999)).toBe("999 B");
    // 1023 is the last value that is still "bytes".
    expect(formatBytes(1023)).toBe("1023 B");
  });

  it("switches unit exactly at each power of 1024", () => {
    expect(formatBytes(1024)).toBe("1.00 KB");
    expect(formatBytes(1024 ** 2)).toBe("1.00 MB");
    expect(formatBytes(1024 ** 3)).toBe("1.00 GB");
    expect(formatBytes(1024 ** 4)).toBe("1.00 TB");
    expect(formatBytes(1024 ** 5)).toBe("1.00 PB");
  });

  it("drops precision as the mantissa grows", () => {
    expect(formatBytes(1536)).toBe("1.50 KB"); // < 10 -> 2 decimals
    expect(formatBytes(10 * 1024)).toBe("10.0 KB"); // >= 10 -> 1 decimal
    expect(formatBytes(100 * 1024)).toBe("100 KB"); // >= 100 -> 0 decimals
  });

  it("clamps at the largest known unit instead of inventing one", () => {
    expect(formatBytes(1024 ** 6)).toBe("1024 PB");
  });
});

describe("formatNumber", () => {
  it("groups thousands", () => {
    expect(formatNumber(1234567)).toBe("1,234,567");
    expect(formatNumber(1000)).toBe("1,000");
  });

  it("passes through small and negative values", () => {
    expect(formatNumber(0)).toBe("0");
    expect(formatNumber(999)).toBe("999");
    expect(formatNumber(-1234)).toBe("-1,234");
  });

  it("returns a placeholder for non-finite input", () => {
    expect(formatNumber(Number.NaN)).toBe("-");
    expect(formatNumber(Number.POSITIVE_INFINITY)).toBe("-");
  });
});

describe("formatCompactNumber", () => {
  it("abbreviates large values", () => {
    expect(formatCompactNumber(1200)).toBe("1.2K");
    expect(formatCompactNumber(1_000_000)).toBe("1M");
  });

  it("leaves values below the first threshold alone", () => {
    expect(formatCompactNumber(0)).toBe("0");
    expect(formatCompactNumber(999)).toBe("999");
  });

  it("returns a placeholder for non-finite input", () => {
    expect(formatCompactNumber(Number.NaN)).toBe("-");
  });
});

describe("formatDate", () => {
  it("returns a placeholder for empty input", () => {
    expect(formatDate(null)).toBe("-");
    expect(formatDate(undefined)).toBe("-");
    expect(formatDate("")).toBe("-");
  });

  it("returns a placeholder for unparseable input", () => {
    expect(formatDate("not-a-date")).toBe("-");
  });

  it("formats an ISO timestamp as a short US date", () => {
    // Asserted loosely on the day so the test does not depend on the host
    // timezone (a midday UTC instant is Mar 14 or Mar 15 anywhere).
    expect(formatDate("2024-03-15T12:00:00Z")).toMatch(/^Mar 1[45], 2024$/);
  });
});

describe("formatDateTime", () => {
  it("returns a placeholder for empty or unparseable input", () => {
    expect(formatDateTime(null)).toBe("-");
    expect(formatDateTime(undefined)).toBe("-");
    expect(formatDateTime("nope")).toBe("-");
  });

  it("adds a time component to the date", () => {
    const out = formatDateTime("2024-03-15T12:00:00Z");
    expect(out).toMatch(/^Mar 1[45], 2024/);
    expect(out).toMatch(/\d{2}:\d{2}/);
  });
});

describe("formatRelativeTime", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  function at(now: string, iso: string): string {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(now));
    return formatRelativeTime(iso);
  }

  it("returns a placeholder for empty or unparseable input", () => {
    expect(formatRelativeTime(null)).toBe("-");
    expect(formatRelativeTime(undefined)).toBe("-");
    expect(formatRelativeTime("nope")).toBe("-");
  });

  it("falls through to seconds below one minute", () => {
    expect(at("2024-01-01T00:00:30Z", "2024-01-01T00:00:00Z")).toBe("30 seconds ago");
    expect(at("2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z")).toBe("now");
  });

  it("picks the largest unit the difference reaches", () => {
    expect(at("2024-01-01T00:01:00Z", "2024-01-01T00:00:00Z")).toBe("1 minute ago");
    expect(at("2024-01-01T02:00:00Z", "2024-01-01T00:00:00Z")).toBe("2 hours ago");
    expect(at("2024-01-03T00:00:00Z", "2024-01-01T00:00:00Z")).toBe("2 days ago");
    expect(at("2024-01-15T00:00:00Z", "2024-01-01T00:00:00Z")).toBe("2 weeks ago");
    expect(at("2024-03-01T00:00:00Z", "2024-01-01T00:00:00Z")).toBe("2 months ago");
    expect(at("2026-01-01T00:00:00Z", "2024-01-01T00:00:00Z")).toBe("2 years ago");
  });

  it("switches unit exactly at the boundary, not one second before", () => {
    expect(at("2024-01-01T00:00:59Z", "2024-01-01T00:00:00Z")).toBe("59 seconds ago");
    expect(at("2024-01-01T00:59:59Z", "2024-01-01T00:00:00Z")).toBe("60 minutes ago");
  });

  it("describes future instants", () => {
    expect(at("2024-01-01T00:00:00Z", "2024-01-01T02:00:00Z")).toBe("in 2 hours");
  });
});

describe("formatDate / formatDateTime with an explicit time zone", () => {
  it("pins the output so a server and a browser in different zones agree", () => {
    // 2026-08-22T20:00Z is already the 23rd in Tokyo: without a fixed zone the
    // two sides of hydration disagree about the calendar day itself.
    const iso = "2026-08-22T20:00:00Z";
    expect(formatDate(iso, "en", "UTC")).toBe(formatDate(iso, "en", "UTC"));
    expect(formatDate(iso, "en", "UTC")).toBe("Aug 22, 2026");
    expect(formatDate(iso, "en", "Asia/Tokyo")).toBe("Aug 23, 2026");
    expect(formatDateTime(iso, "en", "UTC")).toContain("Aug 22, 2026");
  });
});
