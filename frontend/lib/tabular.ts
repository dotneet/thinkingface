/**
 * Client-side parsers for the row-oriented text formats the backend serves as
 * plain text (`previewKind` in backend/internal/api/repotree.go classifies
 * .csv / .tsv / .jsonl as PreviewKindText).
 *
 * These deliberately live in the browser rather than behind a new API: the
 * files are small enough to parse locally, and doing it here keeps the Go side
 * free of format-specific row decoding. Everything below is framework-free so
 * it can be unit-tested under vitest's node environment.
 *
 * The parsers are lenient by design but never silently lie: rows that do not
 * fit the header (or lines that are not valid JSON) are counted in
 * `malformed`, and once too many of them pile up the result flips to
 * `{ ok: false }` so the caller can fall back to the raw text preview instead
 * of showing a table built out of guesses.
 */

export type TabularFormat = "csv" | "tsv" | "jsonl";

export type TabularRow = Record<string, unknown>;

export type TabularTable = {
  format: TabularFormat;
  columns: string[];
  rows: TabularRow[];
  /** Rows dropped or padded because they did not match the header. */
  malformed: number;
  /** True when parsing stopped at MAX_ROWS and later rows were discarded. */
  truncated: boolean;
};

/**
 * Why a file could not be shown as a table. A reason code rather than a
 * sentence: this module is framework-free (no `useT`), so the caller does the
 * translating — see `repo.tabular.parse*` in the dictionaries.
 */
export type TabularParseError =
  | { reason: "noRows" }
  | { reason: "tooManyColumns"; columns: number }
  | { reason: "raggedRows" }
  | { reason: "noJsonObjects" }
  | { reason: "tooManyInvalidLines" };

export type TabularParseResult =
  | { ok: true; table: TabularTable }
  | ({ ok: false } & TabularParseError);

/**
 * Beyond this the table stops being a preview and starts being a memory
 * problem; the caller shows a "truncated" notice rather than an error.
 */
export const MAX_ROWS = 50_000;

/** Wider than this and the horizontal scroll is useless anyway. */
const MAX_COLUMNS = 512;

/**
 * Above this a CSV stops being something to preview in a DOM table: the whole
 * file has to be downloaded and parsed into JS objects first. Files over the
 * limit keep the plain text preview the backend already returned.
 *
 * Lives here (framework-free, not "use client") rather than in
 * tabular-preview.tsx because file-preview.tsx is a Server Component: a
 * Server Component cannot import a value export from a "use client" module
 * (RSC only allows importing components, not values, across that boundary),
 * so the constant needs a home neither side is client-only.
 */
export const MAX_TABULAR_BYTES = 10 * 1024 * 1024;

/**
 * Share of unusable rows above which the file is treated as "not really this
 * format" and the caller falls back to the text preview. A handful of ragged
 * rows in a hand-edited CSV should still render as a table.
 */
const MAX_MALFORMED_RATIO = 0.1;

/**
 * ...but the ratio alone would reject a three-row file with one ragged row, so
 * this many bad rows are always tolerated before the ratio is consulted.
 */
const MALFORMED_GRACE = 2;

/** Returns the table format for a file name, or null when it is not tabular. */
export function tabularFormatFor(fileName: string): TabularFormat | null {
  const lower = fileName.toLowerCase();
  if (lower.endsWith(".csv")) return "csv";
  if (lower.endsWith(".tsv") || lower.endsWith(".tab")) return "tsv";
  if (lower.endsWith(".jsonl") || lower.endsWith(".ndjson")) return "jsonl";
  return null;
}

export function parseTabular(text: string, format: TabularFormat): TabularParseResult {
  if (format === "jsonl") return parseJsonLines(text);
  return parseDelimited(text, format === "tsv" ? "\t" : ",", format);
}

/**
 * Splits RFC 4180-style delimited text into records.
 *
 * Handles quoted fields containing the delimiter, CR/LF, and doubled quotes.
 * A quote in the middle of an unquoted field is kept literally (plenty of
 * real-world exports do that), and an unterminated quote simply ends with the
 * input instead of throwing away the whole file.
 */
export function splitDelimitedRecords(text: string, delimiter: string): string[][] {
  const src = stripBom(text);
  const records: string[][] = [];
  let record: string[] = [];
  let field = "";
  let inQuotes = false;
  let fieldStarted = false;

  const endField = () => {
    record.push(field);
    field = "";
    fieldStarted = false;
  };
  const endRecord = () => {
    endField();
    records.push(record);
    record = [];
  };

  for (let i = 0; i < src.length; i++) {
    const ch = src[i];

    if (inQuotes) {
      if (ch === '"') {
        if (src[i + 1] === '"') {
          field += '"';
          i++;
        } else {
          inQuotes = false;
        }
      } else {
        field += ch;
      }
      continue;
    }

    if (ch === '"' && !fieldStarted) {
      inQuotes = true;
      fieldStarted = true;
      continue;
    }
    if (ch === delimiter) {
      endField();
      continue;
    }
    if (ch === "\r") {
      // Swallow CR only when it is part of a CRLF line break; a lone CR inside
      // a field (rare, but it happens) is kept.
      if (src[i + 1] === "\n") {
        endRecord();
        i++;
        continue;
      }
      endRecord();
      continue;
    }
    if (ch === "\n") {
      endRecord();
      continue;
    }
    field += ch;
    fieldStarted = true;
  }

  // A trailing newline leaves an empty pending record; anything else is a real
  // final row that was not newline-terminated.
  if (field.length > 0 || record.length > 0 || inQuotes) endRecord();

  return records;
}

function parseDelimited(
  text: string,
  delimiter: string,
  format: TabularFormat,
): TabularParseResult {
  const records = splitDelimitedRecords(text, delimiter);
  const header = records.find((r) => !isBlankRecord(r));
  if (!header) return { ok: false, reason: "noRows" };
  if (header.length > MAX_COLUMNS) {
    return { ok: false, reason: "tooManyColumns", columns: header.length };
  }

  const columns = uniqueColumnNames(header);
  const body = records.slice(records.indexOf(header) + 1);

  const rows: TabularRow[] = [];
  let malformed = 0;
  let truncated = false;

  for (const record of body) {
    if (isBlankRecord(record)) continue;
    if (rows.length >= MAX_ROWS) {
      truncated = true;
      break;
    }
    if (record.length !== columns.length) malformed++;
    const row: TabularRow = {};
    for (let i = 0; i < columns.length; i++) {
      // `?? null` rather than "" so a short row reads as "missing", the same
      // way ValueCell renders a Parquet null.
      row[columns[i] as string] = record[i] ?? null;
    }
    rows.push(row);
  }

  if (tooManyMalformed(malformed, rows.length)) {
    return { ok: false, reason: "raggedRows" };
  }
  return { ok: true, table: { format, columns, rows, malformed, truncated } };
}

function parseJsonLines(text: string): TabularParseResult {
  const lines = stripBom(text).split("\n");
  const columns: string[] = [];
  const seen = new Set<string>();
  const rows: TabularRow[] = [];
  let malformed = 0;
  let truncated = false;

  for (const rawLine of lines) {
    const line = rawLine.replace(/\r$/, "").trim();
    if (line === "") continue;
    if (rows.length >= MAX_ROWS) {
      truncated = true;
      break;
    }

    let parsed: unknown;
    try {
      parsed = JSON.parse(line);
    } catch {
      malformed++;
      continue;
    }
    if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
      malformed++;
      continue;
    }

    const row = parsed as TabularRow;
    for (const key of Object.keys(row)) {
      if (seen.has(key) || columns.length >= MAX_COLUMNS) continue;
      seen.add(key);
      columns.push(key);
    }
    rows.push(row);
  }

  if (rows.length === 0) {
    return { ok: false, reason: "noJsonObjects" };
  }
  if (tooManyMalformed(malformed, rows.length)) {
    return { ok: false, reason: "tooManyInvalidLines" };
  }
  return { ok: true, table: { format: "jsonl", columns, rows, malformed, truncated } };
}

function tooManyMalformed(malformed: number, kept: number): boolean {
  if (malformed <= MALFORMED_GRACE) return false;
  const total = malformed + kept;
  return total > 0 && malformed / total > MAX_MALFORMED_RATIO;
}

function isBlankRecord(record: string[]): boolean {
  return record.length === 0 || (record.length === 1 && record[0] === "");
}

/**
 * Column names must be unique because rows are keyed by name. Blank headers
 * become positional names, and repeats get a numeric suffix.
 */
function uniqueColumnNames(header: string[]): string[] {
  const out: string[] = [];
  const used = new Set<string>();
  header.forEach((raw, i) => {
    const base = raw.trim() === "" ? `column_${i + 1}` : raw.trim();
    let name = base;
    let n = 2;
    while (used.has(name)) {
      name = `${base}_${n}`;
      n++;
    }
    used.add(name);
    out.push(name);
  });
  return out;
}

function stripBom(text: string): string {
  return text.charCodeAt(0) === 0xfeff ? text.slice(1) : text;
}

/**
 * Serialises rows back to RFC 4180 CSV, for "copy these results" actions.
 * Objects and arrays (JSONL columns, DuckDB structs) are written as JSON so a
 * round-trip through a spreadsheet keeps something readable.
 */
export function toCsv(columns: string[], rows: TabularRow[]): string {
  const lines = [columns.map(csvField).join(",")];
  for (const row of rows) {
    lines.push(columns.map((col) => csvField(csvValue(row[col]))).join(","));
  }
  return lines.join("\n");
}

function csvValue(value: unknown): string {
  if (value === null || value === undefined) return "";
  if (typeof value === "string") return value;
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}

/**
 * Leading characters that a spreadsheet application (Excel, LibreOffice,
 * Google Sheets) interprets as the start of a formula rather than literal
 * text: `=`, `+`, `-`, `@`, tab, and CR. A cell value under attacker control
 * (an experiment run name, tag, group, or metric key — all of which end up as
 * CSV header or body cells) can smuggle something like
 * `=HYPERLINK("http://evil","click")` through an otherwise-valid export, and
 * it will execute the moment the file is opened. See CWE-1236 / OWASP's "CSV
 * Injection".
 */
const FORMULA_PREFIX = /^[=+\-@\t\r]/;

/**
 * A value that starts with `+` or `-` is legitimately a plain signed number
 * (`-1.5`, `+42`) far more often than it is an attack, so those two prefixes
 * only trigger escaping when the *whole* field is not a number — a genuine
 * `-1.5` must round-trip unchanged. `=`, `@`, tab and CR have no legitimate
 * numeric reading and always trigger escaping.
 */
const PLAIN_NUMBER = /^[+-]?(\d+\.?\d*|\.\d+)(e[+-]?\d+)?$/i;

function needsFormulaEscape(value: string): boolean {
  if (!FORMULA_PREFIX.test(value)) return false;
  if (/^[+-]/.test(value) && PLAIN_NUMBER.test(value)) return false;
  return true;
}

function csvField(value: string): string {
  // A single leading apostrophe is the standard defense: every major
  // spreadsheet app renders it as "force this cell to text" and strips it
  // from the displayed value, so it neutralises the formula without
  // corrupting the data a human or a re-import sees.
  const escaped = needsFormulaEscape(value) ? `'${value}` : value;
  return /["\n\r,]/.test(escaped) ? `"${escaped.replaceAll('"', '""')}"` : escaped;
}
