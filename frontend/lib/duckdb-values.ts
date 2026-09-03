/**
 * Framework- and duckdb-free value normalisation for SQL console results.
 *
 * Split out of lib/duckdb.ts so it can be unit-tested under vitest's node
 * environment: lib/duckdb.ts imports `@duckdb/duckdb-wasm` (a ~35MB
 * browser-only bundle — see its module doc), and even though that import is
 * a dynamic `await import()` reached only from a browser effect, keeping the
 * pure logic in a separate module avoids any risk of dragging that package
 * into a node test run.
 */

/**
 * Above this many bytes a single value is left as a `<N bytes>` placeholder
 * instead of being base64-encoded. A base64 string triples memory pressure
 * (original bytes + intermediate binary string + the encoded string) and one
 * stray huge blob column should not be able to hang the tab.
 */
export const MAX_INLINE_BYTES = 8 * 1024 * 1024;

/**
 * How a temporal Arrow column has to be read. Every one of these arrives as a
 * bare number the table would otherwise print as an integer:
 *
 * - `datetime` (DATE / TIMESTAMP) — epoch milliseconds, an eleven-digit int.
 * - `time` (TIME) — ticks since midnight. Arrow's `getTimeMicrosecond` returns
 *   the element itself, so `SELECT CAST('12:34:56' AS TIME)` used to render as
 *   `45296000000`.
 * - `duration` (DURATION) — the same, as a length rather than a clock reading.
 * - `interval` (INTERVAL) — a *pair* of int32s, which fell through to
 *   `toPlainValue` and printed as `{"0":…,"1":…}`.
 *
 * The column's Arrow type is the only thing that distinguishes any of them
 * from a genuine bigint, so they are matched on the type's string form rather
 * than by importing apache-arrow's type ids (this module deliberately keeps
 * arrow out of the static import graph). The spellings are apache-arrow 17's
 * `DataType#toString`: `Timestamp<MICROSECOND>`, `Date32<DAY>`,
 * `Time64<MICROSECOND>`, `Time32<SECOND>`, `Duration<MICROSECOND>`,
 * `Interval<DAY_TIME>`, `Interval<YEAR_MONTH>`.
 */
export type TemporalKind = "datetime" | "time" | "duration" | "interval";

export function temporalKind(hint: string | undefined): TemporalKind | undefined {
  const type = hint ?? "";
  // `Timestamp` also starts with "Time", so this test has to come first.
  if (/^(Timestamp|Date)/.test(type)) return "datetime";
  if (/^Time(32|64)?(<|$)/.test(type)) return "time";
  if (/^Duration(<|$)/.test(type)) return "duration";
  if (/^Interval(<|$)/.test(type)) return "interval";
  return undefined;
}

export function isTemporalHint(hint: string | undefined): boolean {
  return temporalKind(hint) !== undefined;
}

/** Ticks per second for the TimeUnit named inside the Arrow type string. */
const MILLISECOND = 1000n;
const MICROSECOND = 1000000n;
const TICKS_PER_SECOND: Record<string, bigint> = {
  SECOND: 1n,
  MILLISECOND,
  MICROSECOND,
  NANOSECOND: 1000000000n,
};

function ticksPerSecond(hint: string | undefined): bigint {
  const unit = /<([A-Z_]+)/.exec(hint ?? "")?.[1] ?? "";
  // Microseconds is both DuckDB's own TIME/INTERVAL resolution and Arrow's
  // most common spelling, so it is the safest guess for a type string that
  // somehow carries no unit.
  return TICKS_PER_SECOND[unit] ?? MICROSECOND;
}

/**
 * Renders a tick count as `[-]HH:MM:SS[.fff]`. Used for both a clock reading
 * (TIME, always under 24h) and a length (DURATION, where the hours field is
 * free to run past 24) — the two are the same shape and only differ in how far
 * the first field counts, so one formatter covers both. Digits, colons and a
 * minus sign only: nothing here needs translating.
 *
 * Returns undefined for anything that is not an integer tick count, leaving
 * the caller on its `toPlainValue` fallback.
 */
function formatTicks(value: unknown, perSecond: bigint): string | undefined {
  let ticks: bigint;
  if (typeof value === "bigint") ticks = value;
  else if (typeof value === "number" && Number.isFinite(value)) ticks = BigInt(Math.trunc(value));
  else return undefined;

  const sign = ticks < 0n ? "-" : "";
  const abs = ticks < 0n ? -ticks : ticks;
  const seconds = abs / perSecond;
  const pad = (n: bigint) => String(n).padStart(2, "0");
  const clock = `${pad(seconds / 3600n)}:${pad((seconds % 3600n) / 60n)}:${pad(seconds % 60n)}`;

  const remainder = abs % perSecond;
  if (remainder === 0n) return `${sign}${clock}`;
  // Sub-second digits, trailing zeros trimmed: `.5`, not `.500000`.
  const digits = String(perSecond).length - 1;
  const fraction = String(remainder).padStart(digits, "0").replace(/0+$/, "");
  return `${sign}${clock}.${fraction}`;
}

/**
 * Arrow hands an INTERVAL back as two int32s — `[years, months]` for
 * YEAR_MONTH, `[days, milliseconds]` for DAY_TIME.
 *
 * DAY_TIME folds into the same `HH:MM:SS` reading as a DURATION (a day is
 * 24 hours here, so 1 day 2 hours is `26:00:00`). YEAR_MONTH cannot: months
 * are not a fixed number of seconds, so it renders as the ISO 8601 duration
 * `P1Y2M` — a notation, not prose, so it needs no dictionary entry either.
 */
function formatInterval(value: unknown, hint: string | undefined): string | undefined {
  const pair = value as ArrayLike<unknown> | null | undefined;
  if (typeof pair !== "object" || pair === null || pair.length !== 2) return undefined;
  const [first, second] = [pair[0], pair[1]];
  if (typeof first !== "number" || typeof second !== "number") return undefined;

  if (/YEAR_MONTH/.test(hint ?? "")) {
    if (first === 0 && second === 0) return "P0M";
    return `P${first === 0 ? "" : `${first}Y`}${second === 0 ? "" : `${second}M`}`;
  }
  const MS_PER_DAY = 86400000n;
  return formatTicks(BigInt(first) * MS_PER_DAY + BigInt(second), MILLISECOND);
}

/**
 * Renders one temporal cell. `hint` is the column's Arrow type string; without
 * it the value is read as a DATE/TIMESTAMP, which is what this did before the
 * other three kinds were recognised.
 */
export function toTemporalValue(value: unknown, hint?: string): unknown {
  if (value === null || value === undefined) return null;
  const kind = temporalKind(hint) ?? "datetime";
  if (kind === "time" || kind === "duration") {
    return formatTicks(value, ticksPerSecond(hint)) ?? toPlainValue(value);
  }
  if (kind === "interval") return formatInterval(value, hint) ?? toPlainValue(value);
  if (value instanceof Date) return value.toISOString();
  if (typeof value === "number" || typeof value === "bigint") {
    const date = new Date(Number(value));
    if (!Number.isNaN(date.getTime())) return date.toISOString();
  }
  return toPlainValue(value);
}

/**
 * Arrow hands back BigInt, Date, typed arrays and lazy row proxies. ValueCell
 * eventually calls JSON.stringify on anything object-shaped, which throws on a
 * BigInt, so normalise to plain JSON-safe values here rather than letting one
 * exotic column blow up the whole table.
 *
 * Byte columns become base64 strings (below MAX_INLINE_BYTES) rather than a
 * `<N bytes>` placeholder: the Rows tab (backend) already hands image columns
 * back as base64, and matching that shape here means a `STRUCT(bytes BLOB,
 * path VARCHAR)` result from DuckDB round-trips into the same
 * `{ bytes: "<base64>", path: "..." }` shape ValueCell's image renderer
 * expects, whether the rows came from the API or from a SQL query.
 */
export function toPlainValue(value: unknown, depth = 0): unknown {
  if (value === null || value === undefined) return null;
  if (typeof value === "bigint") {
    // Keep small integers as numbers so they still sort and format as
    // numbers; anything beyond the safe range becomes an exact string.
    return value >= BigInt(Number.MIN_SAFE_INTEGER) && value <= BigInt(Number.MAX_SAFE_INTEGER)
      ? Number(value)
      : value.toString();
  }
  if (value instanceof Date) return value.toISOString();
  if (value instanceof Uint8Array) {
    return value.length > MAX_INLINE_BYTES ? `<${value.length} bytes>` : bytesToBase64(value);
  }
  if (depth >= 4) return String(value);
  if (Array.isArray(value)) return value.map((item) => toPlainValue(item, depth + 1));
  if (typeof value === "object") {
    const source = value as { toJSON?: () => unknown; toArray?: () => unknown[] };
    if (typeof source.toJSON === "function") return fromJsonish(source.toJSON(), depth);
    if (typeof source.toArray === "function") return toPlainValue(source.toArray(), depth + 1);
    const out: Record<string, unknown> = {};
    for (const [key, item] of Object.entries(value)) out[key] = toPlainValue(item, depth + 1);
    return out;
  }
  return value;
}

/**
 * Some Arrow wrappers (DECIMAL's `DecimalBigNum`, notably) return a *JSON
 * fragment* from toJSON rather than a plain value, so the naive path shows
 * `"15"` — quotes included — in the cell. Parse those back into the value they
 * encode, and leave anything that is not JSON alone.
 *
 * apache-arrow renders DECIMAL unscaled (a DECIMAL(21,1) of 1.5 reads as `15`
 * here), because the scale lives on the field type, not the value. This
 * function has no access to that type, so it cannot correct for it — callers
 * that *do* have the column's type string (the `hint` produced in
 * lib/duckdb.ts) must go through `toDecimalValue` below instead, which reads
 * the scale back out of the hint before falling back to this path.
 */
export function fromJsonish(value: unknown, depth: number): unknown {
  if (typeof value !== "string") return toPlainValue(value, depth + 1);
  try {
    const parsed: unknown = JSON.parse(value);
    return typeof parsed === "object" && parsed !== null ? toPlainValue(parsed, depth + 1) : parsed;
  } catch {
    return value;
  }
}

/**
 * Extracts the scale out of an Arrow Decimal type string. apache-arrow 17's
 * `Decimal#toString` renders `Decimal[<precision>e<sign><scale>]` — e.g.
 * `Decimal[10e+2]` for a `DECIMAL(10,2)`, `Decimal[10e0]` for scale 0 — so the
 * exponent after `e` *is* the scale (see node_modules/apache-arrow/type.mjs).
 * Returns undefined for anything else, including a missing hint.
 */
export function decimalScale(hint: string | undefined): number | undefined {
  const match = /^Decimal\[\d+e([+-]?\d+)\]$/.exec(hint ?? "");
  if (!match) return undefined;
  return Number(match[1]);
}

export function isDecimalHint(hint: string | undefined): boolean {
  return decimalScale(hint) !== undefined;
}

/**
 * Renders one DECIMAL cell with its scale applied, using the column's Arrow
 * type string (`hint`, from `lib/duckdb.ts`) to recover the scale that
 * `DecimalBigNum.toJSON()` strips. Without a recognisable hint this falls
 * back to the unscaled `toPlainValue` reading rather than guessing.
 */
export function toDecimalValue(value: unknown, hint?: string): unknown {
  if (value === null || value === undefined) return null;
  const scale = decimalScale(hint);
  if (scale === undefined) return toPlainValue(value);
  const unscaled = unscaledDecimalDigits(value);
  if (unscaled === undefined) return toPlainValue(value);
  return applyDecimalScale(unscaled, scale);
}

/** Pulls the exact unscaled integer (as a digit string) out of a decimal cell. */
function unscaledDecimalDigits(value: unknown): string | undefined {
  if (typeof value === "bigint") return value.toString();
  if (typeof value === "number" && Number.isInteger(value)) return String(value);
  if (typeof value !== "object" || value === null) return undefined;
  const source = value as { toJSON?: () => unknown };
  if (typeof source.toJSON !== "function") return undefined;
  const json = source.toJSON();
  if (typeof json !== "string") return undefined;
  try {
    const parsed: unknown = JSON.parse(json);
    if (typeof parsed === "string" && /^-?\d+$/.test(parsed)) return parsed;
    if (typeof parsed === "number" && Number.isInteger(parsed)) return String(parsed);
  } catch {
    // Not JSON at all; fall through to undefined below.
  }
  return undefined;
}

/**
 * Inserts the decimal point `scale` digits from the right of an exact integer
 * digit string, mirroring how `toPlainValue` treats out-of-range bigints:
 * small results become a JS number (still sorts/formats like a number), and
 * anything whose unscaled part falls outside the safe-integer range stays an
 * exact string rather than being rounded by a float division.
 */
function applyDecimalScale(unscaled: string, scale: number): unknown {
  const negative = unscaled.startsWith("-");
  const digits = negative ? unscaled.slice(1) : unscaled;

  const text =
    scale <= 0
      ? digits + "0".repeat(-scale)
      : (() => {
          const padded = digits.padStart(scale + 1, "0");
          const cut = padded.length - scale;
          return `${padded.slice(0, cut)}.${padded.slice(cut)}`;
        })();
  const signed = negative ? `-${text}` : text;

  // `digits` (the unscaled magnitude) is never signed, so only the upper
  // bound needs checking.
  const safe = BigInt(digits || "0") <= BigInt(Number.MAX_SAFE_INTEGER);
  return safe ? Number(signed) : signed;
}

/**
 * Base64-encodes a byte array without blowing the call stack on
 * `String.fromCharCode(...bytes)` / `.apply` for large inputs — both pass
 * every byte as an individual argument, which V8 caps well under
 * MAX_INLINE_BYTES. Chunking keeps each call small regardless of input size.
 */
export function bytesToBase64(bytes: Uint8Array): string {
  const CHUNK_SIZE = 0x8000;
  let binary = "";
  for (let i = 0; i < bytes.length; i += CHUNK_SIZE) {
    const chunk = bytes.subarray(i, i + CHUNK_SIZE);
    binary += String.fromCharCode(...chunk);
  }
  return btoa(binary);
}
