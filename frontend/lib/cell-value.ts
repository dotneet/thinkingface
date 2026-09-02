/**
 * Shared vocabulary for how a table cell should be rendered.
 *
 * A `CellFeature` is the *frontend* rendering hint derived from a column's
 * metadata — it is intentionally a small closed set, unlike the backend's
 * free-form `ParquetColumn.feature` (the HF `datasets` feature `_type`,
 * lower-cased). `cellFeatureFor` is the single place that maps one to the
 * other, so the viewer, the SQL console and the CSV/JSONL preview agree on
 * what an "image" or "json" column is.
 */

export type CellFeature = "image" | "json";

/** The subset of `ParquetColumn` that decides how its cells render. */
export type CellColumnMeta = {
  /** Parquet logical type annotation, e.g. "STRING" / "JSON" / "LIST"; "" or undefined when none. */
  logical_type?: string;
  /** HF `datasets` feature `_type`, lower-cased ("image", "audio", …); "" or undefined when none. */
  feature?: string;
};

/**
 * Resolves the rendering hint for a column. Returns undefined for plain
 * scalar columns (the common case), which renders as text.
 */
export function cellFeatureFor(col: CellColumnMeta): CellFeature | undefined {
  const feature = (col.feature ?? "").toLowerCase();
  if (feature === "image") return "image";
  if (feature === "json") return "json";
  if ((col.logical_type ?? "").toUpperCase() === "JSON") return "json";
  return undefined;
}

/**
 * Decides whether a cell's value should render as a JSON tree, and with what.
 *
 * Objects and arrays always qualify — a tree is strictly more readable than
 * `{"a":1,"b":[…]}` on one line, however short the value is. Strings only
 * qualify in a column the backend marked as JSON (`logical_type: "JSON"` or
 * the HF `json` feature); a plain Utf8 column that happens to hold `{}` is
 * text, and parsing it would silently change how it reads.
 *
 * Returns undefined when the value is not tree material (scalars, a JSON
 * column holding something unparseable), which leaves the caller on its text
 * path.
 */
export function jsonTreeValueFor(value: unknown, feature?: CellFeature): unknown {
  if (typeof value === "string") return feature === "json" ? parseJsonValue(value) : undefined;
  return parseJsonValue(value);
}

/**
 * The value as a JSON document, or undefined when it is not one.
 *
 * Objects and arrays pass through unchanged; a string is parsed only when it
 * looks like a JSON document (`{` / `[` after trimming) *and* parses. Dates
 * and binary views are excluded: they are `typeof "object"` but reading them
 * as a tree of properties shows implementation detail, not data.
 */
export function parseJsonValue(value: unknown): unknown {
  if (typeof value === "string") {
    const trimmed = value.trim();
    if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) return undefined;
    try {
      return JSON.parse(trimmed);
    } catch {
      return undefined;
    }
  }
  if (typeof value !== "object" || value === null) return undefined;
  if (value instanceof Date || ArrayBuffer.isView(value) || value instanceof ArrayBuffer) {
    return undefined;
  }
  return value;
}

/**
 * One-line text for a cell value. Moved here from ValueCell so the cell, the
 * modal and the tests share one definition of "what this value looks like as
 * text".
 */
export function stringifyValue(value: unknown): string {
  if (value === null || value === undefined) return "null";
  if (typeof value === "string") return value;
  if (typeof value === "object") {
    try {
      return JSON.stringify(value) ?? String(value);
    } catch {
      // Circular structures and BigInt payloads both throw; a cell showing
      // "[object Object]" beats a table that fails to render.
      return String(value);
    }
  }
  return String(value);
}

/** {@link stringifyValue}'s indented sibling, for the modal's Raw view. */
export function prettyJson(value: unknown): string {
  try {
    // BigInt has no JSON representation and throws on its own; Arrow hands
    // one back for every Int64 column, so it reaches here routinely.
    const out = JSON.stringify(
      value,
      (_key, v: unknown) => (typeof v === "bigint" ? v.toString() : v),
      2,
    );
    return out ?? String(value);
  } catch {
    return stringifyValue(value);
  }
}

/** A renderable image extracted from a cell value. */
export type ImageSource = {
  /** `data:` URL, or the remote URL when the value carried one instead of bytes. */
  src: string;
  /** The `path` field of the HF `Image` struct, when present. */
  path: string | null;
  /** Approximate decoded size in bytes, or null when `src` is a remote URL. */
  bytes: number | null;
  /**
   * `src` is an http(s) URL the *dataset* chose, so loading it makes the
   * reader's browser talk to a third-party server the repository owner
   * nominated. Nothing executes (it is an `<img>`), but without a referrer
   * policy that server would also be told the exact page being viewed —
   * private repository path, revision, file and all. Renderers must pass
   * `referrerPolicy="no-referrer"` for these; an inlined `data:` URL makes no
   * request at all and needs nothing.
   */
  external: boolean;
};

// Enough bytes for every signature below: WebP needs 12, the ISO-BMFF brands
// (AVIF / HEIC) need 12, and the SVG text sniff wants a little slack for
// leading whitespace.
const SNIFF_BYTES = 24;

const EXTENSION_MIME: Record<string, string> = {
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  jfif: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  bmp: "image/bmp",
  avif: "image/avif",
  heic: "image/heic",
  heif: "image/heic",
  svg: "image/svg+xml",
  tif: "image/tiff",
  tiff: "image/tiff",
  ico: "image/x-icon",
};

function isHttpUrl(s: string): boolean {
  return /^https?:\/\//i.test(s);
}

/** Strips whitespace that a wrapped base64 payload may carry, and normalises base64url. */
function normalizeBase64(s: string): string {
  return s.replace(/\s+/g, "").replace(/-/g, "+").replace(/_/g, "/");
}

function looksBase64(s: string): boolean {
  // Short strings are almost certainly a label, not an image: the smallest
  // signature this file knows about is 12 bytes = 16 base64 characters.
  if (s.length < 16) return false;
  return /^[A-Za-z0-9+/]+={0,2}$/.test(s);
}

/** Decoded byte length of a (normalised, valid) base64 payload. */
function base64ByteLength(s: string): number {
  const padding = s.endsWith("==") ? 2 : s.endsWith("=") ? 1 : 0;
  return Math.max(0, Math.floor((s.length * 3) / 4) - padding);
}

/** Decodes at most `maxBytes` leading bytes, or null when decoding is unavailable/fails. */
function decodePrefix(base64: string, maxBytes: number): Uint8Array | null {
  if (typeof atob !== "function") return null;
  // atob only accepts whole 4-character groups; take enough of them to cover
  // maxBytes rather than decoding a multi-megabyte payload to read 12 bytes.
  const groups = Math.ceil(maxBytes / 3);
  const head = base64.slice(0, groups * 4);
  const usable = head.length - (head.length % 4);
  if (usable === 0) return null;
  try {
    const binary = atob(head.slice(0, usable));
    const out = new Uint8Array(Math.min(binary.length, maxBytes));
    for (let i = 0; i < out.length; i++) out[i] = binary.charCodeAt(i);
    return out;
  } catch {
    return null;
  }
}

function ascii(bytes: Uint8Array, from: number, length: number): string {
  let out = "";
  for (let i = from; i < from + length && i < bytes.length; i++) {
    out += String.fromCharCode(bytes[i] ?? 0);
  }
  return out;
}

const ISO_BMFF_BRANDS: Record<string, string> = {
  avif: "image/avif",
  avis: "image/avif",
  heic: "image/heic",
  heix: "image/heic",
  hevc: "image/heic",
  hevx: "image/heic",
  mif1: "image/heic",
  msf1: "image/heic",
};

/**
 * Content sniffing over the leading bytes — the value itself is the only
 * reliable source, since the HF `Image` feature carries no MIME and `path` is
 * frequently null (in-memory decoded images) or extension-less.
 */
export function sniffImageMime(bytes: Uint8Array): string | null {
  const b = bytes;
  if (b.length >= 4 && b[0] === 0x89 && b[1] === 0x50 && b[2] === 0x4e && b[3] === 0x47) {
    return "image/png";
  }
  if (b.length >= 3 && b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return "image/jpeg";
  if (ascii(b, 0, 4) === "GIF8") return "image/gif";
  if (ascii(b, 0, 4) === "RIFF" && ascii(b, 8, 4) === "WEBP") return "image/webp";
  if (b.length >= 2 && b[0] === 0x42 && b[1] === 0x4d) return "image/bmp";
  if (ascii(b, 4, 4) === "ftyp") {
    return ISO_BMFF_BRANDS[ascii(b, 8, 4).toLowerCase()] ?? "image/heic";
  }
  const text = ascii(b, 0, b.length).trimStart();
  if (text.startsWith("<svg") || text.startsWith("<?xml")) return "image/svg+xml";
  return null;
}

function mimeFromPath(path: string | null): string | null {
  if (!path) return null;
  const ext = path.split("?")[0]?.split("#")[0]?.split(".").pop()?.toLowerCase() ?? "";
  return EXTENSION_MIME[ext] ?? null;
}

function imageFromParts(raw: string | null, path: string | null): ImageSource | null {
  if (raw !== null && raw !== "") {
    // Already a URL the browser can load: hand it through untouched.
    if (raw.startsWith("data:")) return { src: raw, path, bytes: null, external: false };
    if (isHttpUrl(raw)) return { src: raw, path: path ?? raw, bytes: null, external: true };
    const base64 = normalizeBase64(raw);
    if (!looksBase64(base64)) return null;
    const sniffed = decodePrefix(base64, SNIFF_BYTES);
    // An unrecognised payload still gets a data: URL rather than being
    // dropped — browsers sniff too, and <img onError> covers the rest.
    const mime =
      (sniffed && sniffImageMime(sniffed)) ?? mimeFromPath(path) ?? "application/octet-stream";
    return {
      src: `data:${mime};base64,${base64}`,
      path,
      bytes: base64ByteLength(base64),
      external: false,
    };
  }
  if (path !== null && (isHttpUrl(path) || path.startsWith("data:"))) {
    return { src: path, path, bytes: null, external: isHttpUrl(path) };
  }
  return null;
}

/**
 * Reads a cell value from an `image` column into something renderable.
 *
 * Accepts every shape the backend can produce for the HF `Image` feature: the
 * `{bytes, path}` struct, a bare base64 payload, and a struct whose bytes are
 * null but whose `path` is a URL. Returns null when the value is plainly not
 * an image, so the caller can fall back to its text rendering.
 */
export function imageSourceFor(value: unknown): ImageSource | null {
  if (typeof value === "string") return imageFromParts(value, null);
  if (typeof value !== "object" || value === null) return null;
  const record = value as Record<string, unknown>;
  const raw = typeof record.bytes === "string" ? record.bytes : null;
  const path = typeof record.path === "string" && record.path !== "" ? record.path : null;
  if (raw === null && path === null) return null;
  return imageFromParts(raw, path);
}
