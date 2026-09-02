"use client";

import { Ban, Database, Play, TableProperties } from "lucide-react";
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
  const [running, setRunning] = useState(false);
  const sessionRef = useRef<SqlSession | null>(null);

  useEffect(() => {
    if (tooLarge) return;

    let cancelled = false;
    let session: SqlSession | null = null;

    (async () => {
      try {
        // Straight to the API origin rather than through apiFetch: this is a
        // binary body, not JSON. `credentials: "include"` sends tf_session so
        // authenticated requests work (the backend echoes the Origin and allows
        // credentials — see `cors` in backend/internal/api/server.go).
        const res = await fetch(resolveUrl, { credentials: "include" });
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
  }, [resolveUrl, filePath, tooLarge]);

  const run = useCallback(async () => {
    const session = sessionRef.current;
    if (!session || running) return;
    setRunning(true);
    setQueryError(null);
    try {
      setResult(withSchemaFeatures(await session.query(sql), schemaColumns));
      setResultRun((n) => n + 1);
    } catch (err) {
      // Drop the previous result rather than leaving it on screen: its row
      // count/timing badge and table would otherwise sit next to the new
      // failure and read as if they belonged to the query that just failed.
      setResult(null);
      setQueryError(localizeSqlError(t, err));
    } finally {
      setRunning(false);
    }
  }, [sql, running, t, schemaColumns]);

  if (tooLarge) {
    return (
      <EmptyState
        icon={Ban}
        title={t("parquet.sql.tooLargeTitle")}
        description={t("parquet.sql.tooLargeDescription", {
          size: formatBytes(size),
          max: formatBytes(SQL_CONSOLE_MAX_BYTES),
        })}
      />
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
          pointer on every attempt (see DESIGN.md §8-1). */}
      {!result ? (
        <EmptyState
          icon={Database}
          title={t("parquet.sql.noResultsTitle")}
          description={t("parquet.sql.noResultsDescription")}
        />
      ) : result.columns.length === 0 || result.rows.length === 0 ? (
        <EmptyState
          icon={TableProperties}
          title={t("parquet.sql.noRowsTitle")}
          description={t("parquet.sql.noRowsDescription")}
        />
      ) : (
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
      )}

      {queryError && (
        <Alert tone="negative" title={t("parquet.sql.queryFailed")}>
          <pre className="scroll-x whitespace-pre-wrap break-words font-mono text-xs">
            {queryError}
          </pre>
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
 * (framework-free code cannot call useT); translate those, and show anything
 * else (DuckDB query errors, network failures) verbatim.
 */
function localizeSqlError(t: Translator, err: unknown): string {
  const msg = err instanceof Error ? err.message : typeof err === "string" ? err : null;
  if (msg === null) return t("parquet.sql.unknownError");
  if (msg === "parquet.sql.noWorker" || msg === "parquet.sql.sessionClosed") return t(msg);
  return msg;
}
