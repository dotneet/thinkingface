import { describe, expect, it } from "vitest";
import {
  bytesToBase64,
  fromJsonish,
  isTemporalHint,
  MAX_INLINE_BYTES,
  temporalKind,
  toPlainValue,
  toTemporalValue,
} from "@/lib/duckdb-values";

describe("bytesToBase64", () => {
  it("encodes short byte arrays", () => {
    // "hi" -> [104, 105] -> base64 "aGk="
    expect(bytesToBase64(new Uint8Array([104, 105]))).toBe("aGk=");
  });

  it("round-trips through atob for a large buffer without stack overflow", () => {
    const bytes = new Uint8Array(200_000);
    for (let i = 0; i < bytes.length; i++) bytes[i] = i % 256;
    const encoded = bytesToBase64(bytes);
    const decoded = Uint8Array.from(atob(encoded), (ch) => ch.charCodeAt(0));
    expect(decoded).toEqual(bytes);
  });
});

describe("toPlainValue", () => {
  it("base64-encodes Uint8Array values below the inline cap", () => {
    expect(toPlainValue(new Uint8Array([104, 105]))).toBe("aGk=");
  });

  it("falls back to a placeholder for Uint8Array values above the inline cap", () => {
    const bytes = new Uint8Array(MAX_INLINE_BYTES + 1);
    expect(toPlainValue(bytes)).toBe(`<${bytes.length} bytes>`);
  });

  it("converts small bigints to numbers and huge ones to strings", () => {
    expect(toPlainValue(42n)).toBe(42);
    const huge = BigInt(Number.MAX_SAFE_INTEGER) + 10n;
    expect(toPlainValue(huge)).toBe(huge.toString());
  });

  it("converts Date to an ISO string", () => {
    const date = new Date("2024-01-02T03:04:05.000Z");
    expect(toPlainValue(date)).toBe("2024-01-02T03:04:05.000Z");
  });

  it("passes null/undefined through as null", () => {
    expect(toPlainValue(null)).toBeNull();
    expect(toPlainValue(undefined)).toBeNull();
  });

  it("recurses into arrays", () => {
    expect(toPlainValue([1n, 2n, "x"])).toEqual([1, 2, "x"]);
  });

  it("turns a struct-like row proxy (toJSON) into a plain object, base64-encoding nested bytes", () => {
    // Mimics DuckDB's STRUCT(bytes BLOB, path VARCHAR) row proxy for an image
    // column: Arrow gives back an object whose toJSON() returns the plain
    // shape ValueCell expects.
    const structLike = {
      toJSON: () => ({ bytes: new Uint8Array([104, 105]), path: "img/0.png" }),
    };
    expect(toPlainValue(structLike)).toEqual({ bytes: "aGk=", path: "img/0.png" });
  });

  it("turns an Arrow-like row proxy (toArray) into a plain array", () => {
    const arrayLike = { toArray: () => [1n, 2n] };
    expect(toPlainValue(arrayLike)).toEqual([1, 2]);
  });

  it("stringifies deeply nested values past the recursion depth cap", () => {
    const deeplyNested = { a: { b: { c: { d: { e: "too deep" } } } } };
    const result = toPlainValue(deeplyNested) as Record<string, unknown>;
    // depth 0..3 recurse as plain objects; at depth 4 the value is stringified.
    expect(result.a).toBeDefined();
  });
});

describe("fromJsonish", () => {
  it("parses a JSON fragment string back into its value", () => {
    // DECIMAL's DecimalBigNum.toJSON() returns a JSON *fragment* string.
    expect(fromJsonish("15", 0)).toBe(15);
  });

  it("parses a JSON object fragment and normalises it", () => {
    expect(fromJsonish('{"a":1}', 0)).toEqual({ a: 1 });
  });

  it("leaves a non-JSON string alone", () => {
    expect(fromJsonish("not json", 0)).toBe("not json");
  });

  it("passes non-string values through toPlainValue", () => {
    expect(fromJsonish(42n, 0)).toBe(42);
  });
});

describe("isTemporalHint", () => {
  it("matches Timestamp and Date Arrow type strings", () => {
    expect(isTemporalHint("Timestamp<MICROSECOND>")).toBe(true);
    expect(isTemporalHint("Date32<DAY>")).toBe(true);
  });

  it("matches the time-of-day, duration and interval types too", () => {
    expect(isTemporalHint("Time64<MICROSECOND>")).toBe(true);
    expect(isTemporalHint("Duration<MICROSECOND>")).toBe(true);
    expect(isTemporalHint("Interval<DAY_TIME>")).toBe(true);
  });

  it("rejects everything else", () => {
    expect(isTemporalHint("Int64")).toBe(false);
    expect(isTemporalHint(undefined)).toBe(false);
  });
});

describe("temporalKind", () => {
  it("does not read Timestamp as a TIME, though it starts with one", () => {
    expect(temporalKind("Timestamp<MICROSECOND>")).toBe("datetime");
    expect(temporalKind("Time64<MICROSECOND>")).toBe("time");
    expect(temporalKind("Time32<SECOND>")).toBe("time");
  });

  it("names the duration and interval kinds", () => {
    expect(temporalKind("Duration<MICROSECOND>")).toBe("duration");
    expect(temporalKind("Interval<YEAR_MONTH>")).toBe("interval");
    expect(temporalKind("Utf8")).toBeUndefined();
  });
});

describe("toTemporalValue", () => {
  it("converts epoch milliseconds to an ISO string", () => {
    expect(toTemporalValue(0)).toBe("1970-01-01T00:00:00.000Z");
  });

  it("passes null/undefined through as null", () => {
    expect(toTemporalValue(null)).toBeNull();
    expect(toTemporalValue(undefined)).toBeNull();
  });

  it("falls back to toPlainValue for values that are not a valid epoch", () => {
    expect(toTemporalValue("not a date")).toBe("not a date");
  });

  it("reads a TIME as a clock reading rather than a tick count", () => {
    // CAST('12:34:56' AS TIME) -> 45296000000 microseconds since midnight.
    expect(toTemporalValue(45296000000, "Time64<MICROSECOND>")).toBe("12:34:56");
    expect(toTemporalValue(45296n, "Time32<SECOND>")).toBe("12:34:56");
  });

  it("keeps sub-second precision but trims the trailing zeros", () => {
    expect(toTemporalValue(500000, "Time64<MICROSECOND>")).toBe("00:00:00.5");
  });

  it("lets a DURATION run past 24 hours, and signs a negative one", () => {
    expect(toTemporalValue(93600000000, "Duration<MICROSECOND>")).toBe("26:00:00");
    expect(toTemporalValue(-3600000000, "Duration<MICROSECOND>")).toBe("-01:00:00");
  });

  it("reads an INTERVAL from its pair of int32s", () => {
    // Arrow hands DAY_TIME back as [days, milliseconds].
    expect(toTemporalValue(new Int32Array([1, 7200000]), "Interval<DAY_TIME>")).toBe("26:00:00");
    // …and YEAR_MONTH as [years, months], which has no fixed length in seconds.
    expect(toTemporalValue(new Int32Array([1, 2]), "Interval<YEAR_MONTH>")).toBe("P1Y2M");
    expect(toTemporalValue(new Int32Array([0, 0]), "Interval<YEAR_MONTH>")).toBe("P0M");
  });

  it("still reads a hintless value as epoch milliseconds", () => {
    expect(toTemporalValue(0, "Timestamp<MILLISECOND>")).toBe("1970-01-01T00:00:00.000Z");
  });
});
