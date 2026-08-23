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
 * Arrow renders DATE and TIMESTAMP columns as epoch milliseconds, which would
 * reach the table as an eleven-digit integer. The column's Arrow type is the
 * only thing that distinguishes them from a genuine bigint, so it is matched
 * on the type's string form rather than by importing apache-arrow's type ids
 * (this module deliberately keeps arrow out of the static import graph).
 */
const TEMPORAL_TYPE_RE = /^(Timestamp|Date)/;

export function isTemporalHint(hint: string | undefined): boolean {
  return TEMPORAL_TYPE_RE.test(hint ?? "");
}

export function toTemporalValue(value: unknown): unknown {
  if (value === null || value === undefined) return null;
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
 * (apache-arrow renders DECIMAL unscaled, so a DECIMAL(21,1) of 1.5 still
 * reads as 15. That is upstream behaviour, not something this can recover:
 * the scale lives on the field type, not the value.)
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
