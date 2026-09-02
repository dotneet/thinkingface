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
import { isTemporalHint, toPlainValue, toTemporalValue } from "@/lib/duckdb-values";

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
      const table = await conn.query(sql);
      const elapsedMs = performance.now() - started;

      const columns: DataTableColumn[] = table.schema.fields.map((field) => ({
        key: field.name,
        hint: String(field.type),
      }));
      const rows: DataTableRow[] = [];
      for (const row of table) {
        if (rows.length >= SQL_MAX_RESULT_ROWS) break;
        rows.push(toPlainRow(row, columns));
      }
      return {
        columns,
        rows,
        totalRows: table.numRows,
        truncated: table.numRows > rows.length,
        elapsedMs,
      };
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
    // milliseconds (see temporalKind in lib/duckdb-values.ts).
    out[col.key] = isTemporalHint(col.hint)
      ? toTemporalValue(value, col.hint)
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
