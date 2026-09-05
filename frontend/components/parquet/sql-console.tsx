"use client";

import { Database, Play, RefreshCw, Square, TableProperties } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { CopyButton } from "@/components/ui/copy-button";
import { DataTable } from "@/components/ui/data-table";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Textarea } from "@/components/ui/field";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { cellFeatureFor } from "@/lib/cell-value";
import {
  createParquetSession,
  SQL_CONSOLE_MAX_BYTES,
  SQL_MAX_RESULT_ROWS,
  type SqlResult,
  type SqlSession,
} from "@/lib/duckdb";
import { formatBytes, formatNumber } from "@/lib/format";
import type { Translator } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import { toCsv } from "@/lib/tabular";
import type { ParquetColumn } from "@/types/api";

type Phase = "loading" | "ready" | "error";

/**
 * SQL over the Parquet file currently open in the viewer, executed by
 * DuckDB-WASM in this tab (see lib/duckdb.ts for why it runs client-side).
 *
 * The parent lazy-mounts this only once the SQL panel is opened for the first
 * time: the effect below downloads the whole file and instantiates a ~35MB
 * wasm module, which is not work to do behind a tab nobody opened. After that
 * first mount, the parent keeps this mounted and only hides it (via the
 * `hidden` attribute) when switching back to Rows, so the query text and its
 * result survive a round trip through the other tab instead of forcing a
 * re-download every time.
 */
export function SqlConsole({
  filePath,
  resolveUrl,
  size,
  schemaColumns,
}: {
  /** Repo-relative path of the Parquet file; becomes the queryable name. */
  filePath: string;
  /** Browser-reachable resolve URL for the file (NEXT_PUBLIC_API_URL origin). */
  resolveUrl: string;
  size: number;
  /**
   * The file's schema, from the Rows tab's schema panel. A query result
   * column whose name matches one of these gets that column's rendering
   * hint (image thumbnail, JSON tree), so `SELECT image, label FROM …`
   * renders the same way the Rows tab would.
   */
  schemaColumns: ParquetColumn[];
}) {
  const t = useT();
  const tooLarge = size > SQL_CONSOLE_MAX_BYTES;

  const [phase, setPhase] = useState<Phase>("loading");
  const [initError, setInitError] = useState<string | null>(null);
  const [sql, setSql] = useState("");
  const [result, setResult] = useState<SqlResult | null>(null);
  // Which run produced `result`. Counted rather than derived from the query
  // text: re-running the same SQL still replaces every row in the table (a
  // `random()` or `LIMIT`-less query need not repeat itself), and a counter
  // cannot collide the way a query string could. Feeds `scrollResetKey` below.
  const [resultRun, setResultRun] = useState(0);
  const [queryError, setQueryError] = useState<string | null>(null);
  // Distinguishes "the user stopped this" from "this failed on its own" for
  // the Alert below — both leave `queryError` set (see cancel()), but they
  // are not the same claim and DESIGN.md §9 says not to conflate them. Named
  // apart from the init effect's own local `cancelled` flag below (a
  // same-render-cycle race guard for that effect's cleanup, unrelated to
  // this one) to avoid shadowing it.
  const [queryCancelled, setQueryCancelled] = useState(false);
  const [running, setRunning] = useState(false);
  const sessionRef = useRef<SqlSession | null>(null);
  // Bumped by both run() and cancel() so a run's async continuation can tell
  // whether it is still the one the UI is waiting on. cancel() increments
  // this immediately so run()'s eventual resolution/rejection — DuckDB may
  // settle it well after the user has moved on — is a no-op instead of
  // overwriting the "cancelled" state with a stale result or error.
  const runIdRef = useRef(0);
  // Re-running the init effect below on demand — see the ErrorState `action`
  // in the "error" phase render.
  const [retryKey, setRetryKey] = useState(0);

  useEffect(() => {
    if (tooLarge) return;

    let cancelled = false;
    let session: SqlSession | null = null;

    // Back to square one for the new file. Only nulling `sessionRef` (which is
    // what the cleanup below does) left `phase` at "ready" and the previous
    // file's rows on screen, so `run()` — which returns early without a
    // session — silently did nothing while the console still looked live.
    //
    // Unreachable as things stand: Next 15 keys each route segment's subtree,
    // so opening a different file remounts the viewer and this component with
    // it, and `filePath`/`resolveUrl` never change in place. It becomes
    // reachable the moment they can (a file picker inside the viewer, a
    // client-side swap between two files of one route). Resetting here rather
    // than relying on that remount keeps the effect honest about its own
    // dependencies.
    setPhase("loading");
    setInitError(null);
    setResult(null);
    setQueryError(null);

    (async () => {
      try {
        // Straight to the API origin rather than through apiFetch: this is a
        // binary body, not JSON.
        // `credentials: "omit"`, deliberately. In production this URL answers
        // 302 to a GCS signed URL, and fetch carries the credentials mode
        // across a redirect it follows -- so `include` makes the *bucket*
        // request credentialed too, and a credentialed cross-origin response
        // is only readable when the server answers
        // `Access-Control-Allow-Credentials: true`. GCS never does, whatever
        // CORS the bucket is configured with, so `include` left this fetch
        // permanently unreadable in production while working fine against the
        // dev emulator (which streams the bytes through the API origin
        // instead of redirecting).
        //
        // Omitting them costs nothing: resolve does no permission check --
        // there is no private-repository concept in this system
        // (docs/dev/thinkingface-design.md §11), loadRepoForRead only looks
        // the repository up -- and its download counter is per repository,
        // not per user. If repository visibility is ever introduced, this
        // fetch cannot simply go back to `include`; it needs the signed URL
        // handed over as data and fetched in a second, uncredentialed
        // request.
        const res = await fetch(resolveUrl, { credentials: "omit" });
        if (!res.ok) {
          throw new Error(
            t("parquet.sql.downloadFailed", { status: `${res.status} ${res.statusText}` }),
          );
        }
        const buffer = new Uint8Array(await res.arrayBuffer());
        if (cancelled) return;

        session = await createParquetSession(filePath, buffer);
        if (cancelled) {
          await session.close();
          return;
        }
        sessionRef.current = session;
        setSql(defaultQuery(session.tableName));
        setPhase("ready");
      } catch (err) {
        if (cancelled) return;
        setInitError(localizeSqlError(t, err));
        setPhase("error");
      }
    })();

    return () => {
      cancelled = true;
      sessionRef.current = null;
      void session?.close();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [resolveUrl, filePath, tooLarge, retryKey]);

  const run = useCallback(async () => {
    const session = sessionRef.current;
    if (!session || running) return;
    const runId = ++runIdRef.current;
    setRunning(true);
    setQueryError(null);
    setQueryCancelled(false);
    try {
      const res = await session.query(sql);
      // A cancel() that landed while this was in flight already moved the UI
      // on (and possibly started a newer run) — applying this result now
      // would silently resurrect a query the user asked to stop.
      if (runIdRef.current !== runId) return;
      setResult(withSchemaFeatures(res, schemaColumns));
      setResultRun((n) => n + 1);
    } catch (err) {
      if (runIdRef.current !== runId) return;
      // Drop the previous result rather than leaving it on screen: its row
      // count/timing badge and table would otherwise sit next to the new
      // failure and read as if they belonged to the query that just failed.
      setResult(null);
      setQueryError(localizeSqlError(t, err));
    } finally {
      if (runIdRef.current === runId) setRunning(false);
    }
  }, [sql, running, t, schemaColumns]);

  // Best-effort: interrupts the query DuckDB is currently executing (see
  // SqlSession#cancel in lib/duckdb.ts) and, regardless of whether that
  // interruption actually lands in time, immediately frees the UI — the
  // reported bug was a spinner that only a page reload could stop, because
  // nothing here ever called anything but `disabled` on the Run button.
  const cancel = useCallback(() => {
    const session = sessionRef.current;
    if (!session || !running) return;
    runIdRef.current++;
    setRunning(false);
    setResult(null);
    setQueryCancelled(true);
    setQueryError(t("parquet.sql.cancelled"));
    void session.cancel();
  }, [running, t]);

  // A size refusal is not an empty result (DESIGN.md §9): the file exists and
  // has rows, this tab just cannot run them. A warning Alert says what to do
  // instead (the Rows tab, or a local query) rather than an EmptyState
  // claiming there is nothing here.
  if (tooLarge) {
    return (
      <Alert tone="warning" title={t("parquet.sql.tooLargeTitle")}>
        <p>
          {t("parquet.sql.tooLargeDescription", {
            size: formatBytes(size),
            max: formatBytes(SQL_CONSOLE_MAX_BYTES),
          })}
        </p>
      </Alert>
    );
  }

  if (phase === "loading") {
    return (
      <div className="flex flex-col gap-3">
        <div className="flex items-center gap-2 text-sm text-fg-subtle">
          <Spinner size={14} label={t("parquet.sql.startingDuckDb")} />
          <span>{t("parquet.sql.downloading", { size: formatBytes(size) })}</span>
        </div>
        <Skeleton className="h-28 w-full" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (phase === "error") {
    return (
      <ErrorState
        title={t("parquet.sql.initFailedTitle")}
        message={initError ?? t("parquet.sql.initFailedFallback")}
        hint={t("parquet.sql.initFailedHint")}
        action={
          // A one-off download/wasm-boot failure (a flaky network fetch of
          // the file or the .wasm asset) used to be permanent until the tab
          // reloaded: this component stays mounted for the SQL panel's whole
          // life (see the file doc comment), and getDatabase() only resets
          // its cached promise for the *next* call — nothing here ever made
          // one. Retrying just re-runs the init effect above.
          <Button variant="secondary" size="sm" onClick={() => setRetryKey((n) => n + 1)}>
            <RefreshCw size={13} />
            {t("parquet.retry")}
          </Button>
        }
      />
    );
  }

  return (
    <div className="flex flex-col gap-3">
      <Textarea
        value={sql}
        onChange={(e) => setSql(e.target.value)}
        onKeyDown={(e) => {
          if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
            e.preventDefault();
            void run();
          }
        }}
        spellCheck={false}
        rows={5}
        aria-label={t("parquet.sql.queryAria")}
        className="font-mono text-xs leading-relaxed"
      />

      <div className="flex flex-wrap items-center gap-3">
        <Button variant="primary" onClick={() => void run()} disabled={running}>
          {running ? (
            <Spinner size={13} label={t("parquet.sql.runningQuery")} />
          ) : (
            <Play size={13} />
          )}
          {t("parquet.sql.run")}
        </Button>
        {/* Reserves no space of its own (§8.3 is about layout the reader is
            aiming at; a control that only ever appears next to a Run button
            that is *already* disabled cannot steal a click) — shown only
            while there is something to interrupt. */}
        {running && (
          <Button variant="secondary" onClick={cancel}>
            <Square size={13} />
            {t("parquet.sql.cancel")}
          </Button>
        )}
        <span className="text-xs font-medium text-fg-subtle">
          <kbd className="font-mono">⌘</kbd>/<kbd className="font-mono">Ctrl</kbd> +{" "}
          <kbd className="font-mono">Enter</kbd> {t("parquet.sql.shortcutRuns")}{" "}
          {t("parquet.sql.runsLocally")}
        </span>
        {result && (
          <div className="ml-auto flex items-center gap-3 text-xs font-medium tabular-nums text-fg-subtle">
            <span>
              {t(
                result.totalRows === 1
                  ? "parquet.sql.resultStatsOne"
                  : "parquet.sql.resultStatsOther",
                { count: formatNumber(result.totalRows), ms: result.elapsedMs.toFixed(0) },
              )}
            </span>
            <CopyButton
              label={t("parquet.sql.copyCsv")}
              value={() =>
                toCsv(
                  result.columns.map((c) => c.key),
                  result.rows,
                )
              }
            />
          </div>
        )}
      </div>

      {/* Run button and result table stay adjacent: both messages below react
          to a run but must not land between the button and the table, or
          fixing a query and re-running would shift the table under the
          pointer on every attempt (see DESIGN.md §8-1).
          "No results yet" and "the query failed" are two different claims
          (DESIGN.md §9) — showing the former on top of the failure Alert
          below said both "nothing has run" and "something just failed" at
          once, so it renders only when neither a result nor an error is on
          screen. A cancelled run also has `queryError` set (see cancel()
          above) and takes the same branch as a real failure, which is
          correct: its result really is unknown, not empty. */}
      {!result && !queryError ? (
        <EmptyState
          icon={Database}
          title={t("parquet.sql.noResultsTitle")}
          description={t("parquet.sql.noResultsDescription")}
        />
      ) : result && (result.columns.length === 0 || result.rows.length === 0) ? (
        <EmptyState
          icon={TableProperties}
          title={t("parquet.sql.noRowsTitle")}
          description={t("parquet.sql.noRowsDescription")}
        />
      ) : result ? (
        <DataTable
          columns={result.columns}
          rows={result.rows}
          // Every run replaces the rows wholesale, so the box scrolls back to
          // the top: a result up to SQL_MAX_RESULT_ROWS rows tall is far
          // taller than the box, and re-running while scrolled down otherwise
          // opened the new result part-way through it, with its first rows
          // already scrolled past (DESIGN.md §8 — the rows the reader is
          // looking at must not silently change under a scroll position that
          // no longer means anything). Same reason as the Rows tab's paging.
          scrollResetKey={resultRun}
        />
      ) : null}

      {queryError && (
        <Alert
          tone={queryCancelled ? "warning" : "negative"}
          title={t(queryCancelled ? "parquet.sql.cancelledTitle" : "parquet.sql.queryFailed")}
        >
          {queryCancelled ? (
            // Our own copy, already translated — nothing to detach.
            <p>{queryError}</p>
          ) : (
            <>
              <p>{t("parquet.sql.queryFailedBody")}</p>
              {/* The engine's own text stays available but collapsed: DuckDB
                  errors name functions and files in English, so they are the
                  detail, not the message (same <details>/<pre> idiom as
                  MetadataValue in components/model/model-metadata-table.tsx).
                  CopyButton sits outside the <details> so the text is
                  retrievable without expanding it first. */}
              <details>
                <summary className="cursor-pointer text-xs font-medium text-fg-subtle hover:text-fg">
                  {t("parquet.sql.showDetail")}
                </summary>
                <pre className="scroll-x mt-1 max-h-64 overflow-y-auto whitespace-pre-wrap break-words rounded-md bg-bg-sunken p-2 font-mono text-xs">
                  {queryError}
                </pre>
              </details>
              <CopyButton value={queryError} label={t("parquet.sql.copyError")} />
            </>
          )}
        </Alert>
      )}

      {result?.truncated && (
        <Alert tone="warning">
          {t("parquet.sql.truncated", {
            limit: formatNumber(SQL_MAX_RESULT_ROWS),
            total: formatNumber(result.totalRows),
          })}
        </Alert>
      )}
    </div>
  );
}

function defaultQuery(tableName: string): string {
  return `SELECT *\nFROM '${tableName}'\nLIMIT 100;`;
}

/**
 * Attaches the schema's rendering hint to result columns whose name matches
 * a schema column — a plain `SELECT image, label FROM …` then renders the
 * image column as a thumbnail the same way the Rows tab does. Anything the
 * query computed or renamed (aliases, expressions) has no schema match and
 * falls back to text, same as before this existed.
 */
function withSchemaFeatures(result: SqlResult, schemaColumns: ParquetColumn[]): SqlResult {
  if (schemaColumns.length === 0) return result;
  const schemaByName = new Map(schemaColumns.map((c) => [c.name, c]));
  return {
    ...result,
    columns: result.columns.map((col) => {
      const schemaCol = schemaByName.get(col.key);
      return schemaCol ? { ...col, feature: cellFeatureFor(schemaCol) } : col;
    }),
  };
}

/**
 * lib/duckdb.ts throws its own known failures with an i18n key as the message
 * (framework-free code cannot call useT); translate those. Anything else
 * (DuckDB query errors, network failures) comes back as the raw detail —
 * callers render it inside a collapsed `<details>`, never as the message
 * itself.
 */
function localizeSqlError(t: Translator, err: unknown): string {
  const msg = err instanceof Error ? err.message : typeof err === "string" ? err : null;
  if (msg === null) return t("parquet.sql.unknownError");
  if (msg === "parquet.sql.noWorker" || msg === "parquet.sql.sessionClosed") return t(msg);
  return msg;
}
