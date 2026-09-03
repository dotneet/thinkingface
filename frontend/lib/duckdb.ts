/**
 * Browser-side SQL over Parquet, via DuckDB-WASM.
 *
 * The backend is deliberately pure Go (no CGo — see invariant 4 in CLAUDE.md),
 * so there is no SQL engine on the server and there never will be one behind
 * `internal/viewer`. Running DuckDB in the *browser* answers the "what if we
 * need SQL filters" question from §9 of the design doc without touching that
 * constraint: the page fetches the Parquet bytes from the ordinary resolve
 * endpoint and queries them locally, so the server keeps handing out files and
 * nothing else.
 *
 * Everything here is browser-only and dynamically imported, so the ~35MB
 * DuckDB bundle never lands in the initial JS payload and never runs during
 * SSR or `next build`.
 *
 * ## Asset hosting
 *
 * The wasm module and its worker are served from `/duckdb/*` on our own origin
 * (scripts/copy-duckdb-assets.mjs copies them out of node_modules into
 * public/duckdb/ before every build). We do *not* use
 * `duckdb.getJsDelivrBundles()`: a self-hosted deployment must not depend on a
 * third-party CDN being reachable, and `new Worker(url)` on a cross-origin URL
 * is blocked by the browser anyway.
 */

import type { AsyncDuckDB, AsyncDuckDBConnection, DuckDBBundles } from "@duckdb/duckdb-wasm";
import type { DataTableColumn, DataTableRow } from "@/components/ui/data-table";
import {
  isDecimalHint,
  isTemporalHint,
  toDecimalValue,
  toPlainValue,
  toTemporalValue,
} from "@/lib/duckdb-values";

/**
 * Above this the download alone would be hostile, never mind decoding it in a
 * tab. The SQL console refuses rather than hanging the browser.
 */
export const SQL_CONSOLE_MAX_BYTES = 500 * 1024 * 1024;

/** Hard cap on rows materialized out of an Arrow result into JS objects. */
export const SQL_MAX_RESULT_ROWS = 10_000;

const ASSET_BASE = "/duckdb";

// COI is intentionally absent: the threaded bundle needs COOP/COEP headers we
// do not send, and selectBundle() would pick it whenever a browser happens to
// report crossOriginIsolated.
const SELF_HOSTED_BUNDLES: DuckDBBundles = {
  mvp: {
    mainModule: `${ASSET_BASE}/duckdb-mvp.wasm`,
    mainWorker: `${ASSET_BASE}/duckdb-browser-mvp.worker.js`,
  },
  eh: {
    mainModule: `${ASSET_BASE}/duckdb-eh.wasm`,
    mainWorker: `${ASSET_BASE}/duckdb-browser-eh.worker.js`,
  },
};

export type SqlResult = {
  columns: DataTableColumn[];
  rows: DataTableRow[];
  /** Rows the query produced, even if only SQL_MAX_RESULT_ROWS were kept. */
  totalRows: number;
  truncated: boolean;
  elapsedMs: number;
};

export type SqlSession = {
  /** Name the Parquet file is registered under; interpolate into SQL as-is. */
  readonly tableName: string;
  query(sql: string): Promise<SqlResult>;
  /** Best-effort interruption of a query() call currently in flight. */
  cancel(): Promise<void>;
  close(): Promise<void>;
};

// One database per tab, shared by every session: instantiating the wasm module
// costs seconds and tens of megabytes, and DuckDB is happy to hold several
// registered files and connections at once.
let dbPromise: Promise<AsyncDuckDB> | null = null;

async function getDatabase(): Promise<AsyncDuckDB> {
  if (!dbPromise) {
    dbPromise = instantiate().catch((err) => {
      // Drop the rejected promise so a retry (a reload of the panel, a flaky
      // network on the .wasm fetch) can start over instead of replaying the
      // same failure forever.
      dbPromise = null;
      throw err;
    });
  }
  return dbPromise;
}

async function instantiate(): Promise<AsyncDuckDB> {
  // Note: next.config.ts lists this package in `serverExternalPackages`. The
  // server build resolves the bare specifier through the "node" export
  // condition to duckdb-node.cjs, whose dynamic requires webpack cannot
  // analyse; marking it external keeps that module out of the server bundle.
  // Nothing here ever executes on the server — the import is reached only from
  // a browser effect — and the *subpath* that would sidestep the condition
  // (dist/duckdb-browser) ships no types under "exports".
  const duckdb = await import("@duckdb/duckdb-wasm");
  const bundle = await duckdb.selectBundle(SELF_HOSTED_BUNDLES);
  if (!bundle.mainWorker) {
    // Message is an i18n key; the SQL console translates it before display
    // (see localizeSqlError in components/parquet/sql-console.tsx).
    throw new Error("parquet.sql.noWorker");
  }
  const worker = new Worker(bundle.mainWorker);
  try {
    const db = new duckdb.AsyncDuckDB(new duckdb.ConsoleLogger(duckdb.LogLevel.WARNING), worker);
    await db.instantiate(bundle.mainModule, bundle.pthreadWorker);
    return db;
  } catch (err) {
    // getDatabase() drops the rejected promise so the next attempt starts
    // over — which is exactly why the worker of the failed attempt has to go
    // with it. A failing .wasm fetch left one live worker per retry, each
    // holding its own wasm heap, until the tab was reloaded.
    worker.terminate();
    throw err;
  }
}

/**
 * How many live sessions reference each registered buffer name.
 *
 * The name is derived from the file path so the default query reads like the
 * file the user opened (`SELECT * FROM 'train.parquet'`), which means a remount
 * — React StrictMode does one on every mount in development — briefly has two
 * sessions on the same name. Without refcounting the first one's teardown would
 * drop the buffer out from under the second.
 */
const registrations = new Map<string, number>();

/**
 * Registers `data` with DuckDB under a name derived from `filePath` and opens
 * a connection against it. The returned `tableName` is what queries should
 * select from: `SELECT * FROM 'train.parquet'`.
 */
export async function createParquetSession(
  filePath: string,
  data: Uint8Array,
): Promise<SqlSession> {
  const db = await getDatabase();
  const tableName = safeName(filePath);
  // Note: this transfers the buffer to the worker, leaving `data` detached.
  // Callers must not reuse it afterwards.
  //
  // Registered *before* the refcount goes up, and the connection is opened
  // under a rollback: a failure on either side used to leave the count raised
  // for a session that never existed, so the buffer it named could never be
  // dropped again and pinned the whole file in wasm memory for the life of
  // the tab.
  await db.registerFileBuffer(tableName, data);
  registrations.set(tableName, (registrations.get(tableName) ?? 0) + 1);
  let conn: AsyncDuckDBConnection;
  try {
    conn = await db.connect();
  } catch (err) {
    await release(db, tableName);
    throw err;
  }

  let closed = false;
  return {
    tableName,
    async query(sql: string): Promise<SqlResult> {
      // i18n key, translated by the SQL console before display.
      if (closed) throw new Error("parquet.sql.sessionClosed");
      const started = performance.now();
      // `send` rather than `query`: `query` issues a single RUN_QUERY worker
      // task that runs the whole query as one blocking wasm call, which the
      // worker's message loop cannot interrupt mid-flight — a `cancel()`
      // call queued while it runs simply waits for it to finish. `send`
      // drives DuckDB's pending-query protocol instead (start, then poll
      // until ready), which executes in interruptible steps and is what
      // `cancel()` below (AsyncDuckDBConnection#cancelSent) is designed to
      // interrupt between steps.
      //
      // `open()` is not optional here the way it is for the readers
      // `RecordBatchReader.from()` hands back already opened: `send` returns
      // an AsyncRecordBatchStreamReader that has not yet read the stream's
      // schema message, so `reader.schema` is undefined until this resolves
      // and the `.fields` access below throws.
      const reader = await (await conn.send(sql)).open();
      const columns: DataTableColumn[] = reader.schema.fields.map((field) => ({
        key: field.name,
        hint: String(field.type),
      }));
      const rows: DataTableRow[] = [];
      let totalRows = 0;
      // Every batch is drained (not just up to SQL_MAX_RESULT_ROWS) so
      // `totalRows` still reports the query's true row count for the
      // "N of M rows shown" banner. Turning rows into JS objects stops at the
      // cap, and so does walking them: `numRows` is read off the batch, so
      // nothing past the cap has to be visited to count it, and iterating a
      // batch materialises an Arrow proxy per row — a cost worth paying only
      // for rows that will be displayed.
      for await (const batch of reader) {
        totalRows += batch.numRows;
        if (rows.length >= SQL_MAX_RESULT_ROWS) continue;
        for (const row of batch) {
          rows.push(toPlainRow(row, columns));
          if (rows.length >= SQL_MAX_RESULT_ROWS) break;
        }
      }
      const elapsedMs = performance.now() - started;
      return {
        columns,
        rows,
        totalRows,
        truncated: totalRows > rows.length,
        elapsedMs,
      };
    },
    // Best-effort: interrupts a query started by the query() call currently
    // in flight on this connection, per the trace above. If nothing is
    // running (or the query has already moved past the interruptible
    // pending-query phase into pure result transfer), this is a harmless
    // no-op — the caller does not have to know which case it is.
    async cancel(): Promise<void> {
      await conn.cancelSent().catch(() => {});
    },
    async close(): Promise<void> {
      if (closed) return;
      closed = true;
      await closeQuietly(conn);
      await release(db, tableName);
    },
  };
}

/**
 * Gives up one reference to a registered buffer, freeing it once the last one
 * goes. Leaving it registered would pin the whole file in wasm memory for the
 * lifetime of the tab.
 */
async function release(db: AsyncDuckDB, tableName: string): Promise<void> {
  const remaining = (registrations.get(tableName) ?? 1) - 1;
  if (remaining > 0) {
    registrations.set(tableName, remaining);
    return;
  }
  registrations.delete(tableName);
  await db.dropFile(tableName).catch(() => {});
}

async function closeQuietly(conn: AsyncDuckDBConnection): Promise<void> {
  try {
    await conn.close();
  } catch {
    // Closing a connection whose worker already went away is not worth
    // surfacing; the session is being torn down either way.
  }
}

function toPlainRow(row: unknown, columns: DataTableColumn[]): DataTableRow {
  const source = row as Record<string, unknown> | null;
  const out: DataTableRow = {};
  for (const col of columns) {
    const value = source?.[col.key];
    // The hint goes in as well as gating the call: TIME, DURATION and INTERVAL
    // are temporal too, and each is read differently from a TIMESTAMP's epoch
    // milliseconds (see temporalKind in lib/duckdb-values.ts). DECIMAL is the
    // same story: apache-arrow hands it back unscaled, and the scale to apply
    // lives in this same hint string (see decimalScale).
    out[col.key] = isTemporalHint(col.hint)
      ? toTemporalValue(value, col.hint)
      : isDecimalHint(col.hint)
        ? toDecimalValue(value, col.hint)
        : toPlainValue(value);
  }
  return out;
}

/**
 * DuckDB addresses registered buffers by exact name inside a single-quoted SQL
 * literal, so strip anything that would end that literal or read as a path.
 * Directory separators become underscores rather than being dropped, so
 * `train/0000.parquet` and `test/0000.parquet` stay distinguishable.
 */
function safeName(filePath: string): string {
  const cleaned = filePath.replace(/[^A-Za-z0-9._-]/g, "_").replace(/^_+/, "");
  return cleaned === "" ? "data.parquet" : cleaned.slice(-80);
}
