"use client";

import { useQuery } from "@tanstack/react-query";
import {
  ChevronLeft,
  ChevronRight,
  ChevronsLeft,
  Database,
  RefreshCw,
  Rows3,
  Terminal,
} from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { SchemaPanel } from "@/components/parquet/schema-panel";
import { SqlConsole } from "@/components/parquet/sql-console";
import { Button } from "@/components/ui/button";
import { DataTable } from "@/components/ui/data-table";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Select } from "@/components/ui/field";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { Skeleton } from "@/components/ui/skeleton";
import { SpinnerSlot } from "@/components/ui/spinner";
import { ApiResultError, queryErrorMessage } from "@/lib/api-error-message";
import { cellFeatureFor } from "@/lib/cell-value";
import { formatBytes, formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";
import { getParquetRows } from "@/lib/parquet";
import { publicApiBase } from "@/lib/paths";
import { resolveFileUrl } from "@/lib/repos";
import type { ParquetSchemaResponse, RepoKind } from "@/types/api";

const PAGE_SIZES = [50, 100, 200, 500];
type Row = Record<string, unknown>;
type Mode = "rows" | "sql";

export function ParquetViewer({
  kind,
  ns,
  name,
  rev,
  path,
  schema,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
  path: string[];
  schema: ParquetSchemaResponse;
}) {
  const t = useT();
  const [mode, setMode] = useState<Mode>("rows");
  // Lazy-mounted the first time SQL is opened, then left mounted (see the
  // render below): re-mounting on every tab switch would re-download the
  // file and re-boot the wasm module each time you flip back.
  const [sqlOpened, setSqlOpened] = useState(false);
  const [hidden, setHidden] = useState<Set<string>>(new Set());
  const [limit, setLimit] = useState(100);
  const [offset, setOffset] = useState(0);

  function handleModeChange(next: Mode) {
    setMode(next);
    if (next === "sql") setSqlOpened(true);
  }

  const visibleColumnNames = useMemo(
    () => schema.columns.filter((c) => !hidden.has(c.name)).map((c) => c.name),
    [schema.columns, hidden],
  );

  // Debounced ~300ms before it reaches the query key/fetch: toggling several
  // column checkboxes in a row would otherwise fire one server round-trip per
  // click. The checkboxes themselves (via `hidden`, passed to SchemaPanel)
  // still update immediately — only the fetched/rendered column set lags.
  const [debouncedColumnNames, setDebouncedColumnNames] = useState(visibleColumnNames);
  useEffect(() => {
    const id = setTimeout(() => setDebouncedColumnNames(visibleColumnNames), 300);
    return () => clearTimeout(id);
  }, [visibleColumnNames]);

  // Only the Rows tab talks to the backend viewer API; the SQL tab pulls the
  // file itself and queries it in the browser, so pause this query while it is
  // showing rather than paging in the background.
  const { data, isFetching, isError, error, refetch } = useQuery({
    // Skipped with every column hidden: `getParquetRows` drops an empty
    // `columns` list from the query string, and the backend reads that as
    // "no column filter" -- so "hide all" would fetch a full page of every
    // column just to render the no-columns empty state.
    enabled: mode === "rows" && debouncedColumnNames.length > 0,
    queryKey: [
      "parquet-rows",
      kind,
      ns,
      name,
      rev,
      path.join("/"),
      offset,
      limit,
      // JSON, not a comma-join: a column literally named `a,b` would collide
      // with the pair `a` + `b` in the cache key.
      JSON.stringify(debouncedColumnNames),
    ],
    queryFn: async () => {
      const result = await getParquetRows(kind, ns, name, rev, path, {
        offset,
        limit,
        columns: debouncedColumnNames,
      });
      // ApiResultError (not a bare Error) so the render below can translate
      // the backend's `error.type` instead of showing raw English ([S12]).
      if (!result.ok) throw new ApiResultError(result);
      return result.data;
    },
    placeholderData: (prev) => prev,
  });

  function toggleColumn(colName: string) {
    setHidden((prev) => {
      const next = new Set(prev);
      if (next.has(colName)) next.delete(colName);
      else next.add(colName);
      return next;
    });
  }

  // Bulk show/hide for SchemaPanel's "show all / hide all": `names` is the
  // set it currently has in view (all columns, or the filtered subset).
  function setColumnsHidden(names: string[], hide: boolean) {
    setHidden((prev) => {
      const next = new Set(prev);
      for (const n of names) {
        if (hide) next.add(n);
        else next.delete(n);
      }
      return next;
    });
  }

  // The one response the table is actually drawing. `placeholderData` keeps the
  // previous page on screen for the whole round trip and react-query keeps the
  // last successful `data` after a failure, so *everything the table says about
  // itself* — its columns, its row range, its total — is read from here rather
  // than from the offset/limit/column state, which has already moved on
  // (DESIGN.md §9: "not fetched yet" is not "empty" and is not "failed").
  const page = isError ? undefined : data;
  const rows: Row[] = page?.rows ?? [];

  // From the response, not from `debouncedColumnNames`: un-hiding a column
  // updates the state immediately while `rows` is still the placeholder page
  // fetched for the previous column set, so a state-derived column list
  // painted a column of `undefined` — which ValueCell used to render as the
  // string "null", indistinguishable from a column that really is null.
  // The response's columns also carry the README-resolved `feature`, which is
  // what decides image/JSON rendering.
  const columns = useMemo(
    () => (page?.columns ?? []).map((col) => ({ key: col.name, feature: cellFeatureFor(col) })),
    [page],
  );

  const totalRows = page?.num_rows ?? schema.num_rows;
  const hasPrev = offset > 0;
  const hasNext = offset + limit < totalRows;
  // Authoritative offset/limit from the response; `rows.length` rather than
  // `limit` for the upper bound so a short final page reads correctly.
  const pageFrom = page && rows.length > 0 ? page.offset + 1 : 0;
  const pageTo = page ? page.offset + rows.length : 0;

  const resolveUrl = resolveFileUrl(kind, ns, name, rev, path, publicApiBase());

  const modes = [
    { value: "rows" as const, label: t("parquet.viewer.modeRows"), icon: Rows3 },
    { value: "sql" as const, label: t("parquet.viewer.modeSql"), icon: Terminal },
  ];

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-4 text-sm text-fg-subtle">
        <span className="tabular-nums">
          {t(totalRows === 1 ? "parquet.viewer.rowsOne" : "parquet.viewer.rowsOther", {
            count: formatNumber(totalRows),
          })}
        </span>
        <span className="tabular-nums">{formatBytes(schema.size)}</span>
        <span>
          {t(
            schema.num_row_groups === 1
              ? "parquet.viewer.rowGroupsOne"
              : "parquet.viewer.rowGroupsOther",
            { count: schema.num_row_groups },
          )}
        </span>
        {schema.compression && <span>{schema.compression}</span>}
        <SegmentedControl
          className="ml-auto"
          label={t("parquet.viewer.viewerMode")}
          value={mode}
          options={modes}
          onChange={handleModeChange}
        />
      </div>

      <div className="flex flex-col gap-4 lg:flex-row">
        <SchemaPanel
          columns={schema.columns}
          hidden={hidden}
          // Read-only under SQL: what a query returns is decided by its SELECT
          // list, not by these checkboxes.
          onToggle={mode === "rows" ? toggleColumn : undefined}
          onSetHidden={mode === "rows" ? setColumnsHidden : undefined}
        />

        <div className="flex min-w-0 flex-1 flex-col gap-3">
          {/* SQL lazy-mounts on first open (SqlConsole downloads the file and
              boots a ~35MB wasm module — see lib/duckdb.ts) but, once opened,
              stays mounted for the rest of the session: switching back to Rows
              and then back to SQL just re-shows it via the `hidden` attribute
              instead of re-downloading everything and losing the query.
              Tailwind's preflight makes `[hidden]` `display:none !important`,
              which also drops the panel from the tab order and the
              accessibility tree, so no separate aria-hidden/inert is needed. */}
          {sqlOpened && (
            <div hidden={mode !== "sql"} className="flex flex-col gap-3">
              <SqlConsole
                filePath={path.join("/")}
                resolveUrl={resolveUrl}
                size={schema.size}
                schemaColumns={schema.columns}
              />
            </div>
          )}
          {mode === "rows" && (
            <>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-2 text-sm">
                  <span className="text-fg-subtle">{t("parquet.viewer.rowsPerPage")}</span>
                  <Select
                    value={limit}
                    onChange={(e) => {
                      setLimit(Number(e.target.value));
                      setOffset(0);
                    }}
                    // The preceding <span> is a visual label only, not a
                    // <label for>, so this had no accessible name of its own.
                    aria-label={t("parquet.viewer.rowsPerPage")}
                    className="w-auto px-2 py-1 text-sm"
                  >
                    {PAGE_SIZES.map((size) => (
                      <option key={size} value={size}>
                        {size}
                      </option>
                    ))}
                  </Select>
                  <SpinnerSlot
                    active={isFetching}
                    size={14}
                    label={t("parquet.viewer.loadingRows")}
                  />
                </div>
                <div className="flex items-center gap-2 text-sm">
                  {/* A range is only ever stated from a response that is on
                      screen: none while the first page is still in flight, and
                      none at all when the fetch failed — "Showing 201–300 of
                      4,000,000" directly above "failed to load rows" is §9
                      rule 1. The slot keeps its width so the buttons beside it
                      do not move when the text appears (§8.3). */}
                  <span className="min-w-[14ch] text-right tabular-nums text-fg-subtle">
                    {page
                      ? t("parquet.viewer.pageInfo", {
                          from: pageFrom,
                          to: pageTo,
                          total: formatNumber(page.num_rows),
                        })
                      : null}
                  </span>
                  <Button
                    size="sm"
                    disabled={!hasPrev}
                    onClick={() => setOffset(0)}
                    aria-label={t("parquet.viewer.firstPage")}
                  >
                    <ChevronsLeft size={14} />
                  </Button>
                  <Button
                    size="sm"
                    disabled={!hasPrev}
                    onClick={() => setOffset((o) => Math.max(0, o - limit))}
                    aria-label={t("parquet.viewer.prevPage")}
                  >
                    <ChevronLeft size={14} />
                  </Button>
                  <Button
                    size="sm"
                    disabled={!hasNext}
                    onClick={() => setOffset((o) => o + limit)}
                    aria-label={t("parquet.viewer.nextPage")}
                  >
                    <ChevronRight size={14} />
                  </Button>
                </div>
              </div>

              {isError ? (
                <ErrorState
                  title={t("parquet.errorTitle")}
                  message={queryErrorMessage(t, error, t("parquet.viewer.loadRowsFailed"))}
                  action={
                    // react-query's own retry (QueryClient default: retry: 1,
                    // see app/providers.tsx) already ran once by the time this
                    // renders; without this, a transient 5xx stayed failed
                    // until the reader navigated away and back or reloaded.
                    <Button variant="secondary" size="sm" onClick={() => void refetch()}>
                      <RefreshCw size={13} />
                      {t("parquet.retry")}
                    </Button>
                  }
                />
              ) : debouncedColumnNames.length === 0 ? (
                // The *selection*, not the response: with every column hidden
                // the query above is disabled, so there is no response to read
                // this from.
                <EmptyState
                  icon={Database}
                  title={t("parquet.viewer.noColumnsTitle")}
                  description={t("parquet.viewer.noColumnsDescription")}
                />
              ) : !page ? (
                // No page has arrived yet (first paint, or the first paint
                // after unhiding a column from an empty selection). Not empty
                // and not failed — a placeholder, per DESIGN.md §4.
                <Skeleton className="h-96 w-full" />
              ) : rows.length === 0 ? (
                <EmptyState icon={Database} title={t("parquet.viewer.noRowsTitle")} />
              ) : (
                <DataTable
                  columns={columns}
                  rows={rows}
                  // A new page is a different set of rows, so the box scrolls
                  // back to the top: a 100-row page is taller than the box,
                  // and paging while scrolled down otherwise opened the next
                  // page part-way through it (DESIGN.md §8 — the rows the
                  // reader is looking at must not silently change under a
                  // scroll position that no longer means anything).
                  scrollResetKey={`${offset}:${limit}`}
                />
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
}
