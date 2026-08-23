"use client";

import { useQuery } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, ChevronsLeft, Database, Rows3, Terminal } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { SchemaPanel } from "@/components/parquet/schema-panel";
import { SqlConsole } from "@/components/parquet/sql-console";
import { Button } from "@/components/ui/button";
import { DataTable } from "@/components/ui/data-table";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Select } from "@/components/ui/field";
import { SegmentedControl } from "@/components/ui/segmented-control";
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
  const { data, isFetching, isError, error } = useQuery({
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
      debouncedColumnNames.join(","),
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

  const rows: Row[] = data?.rows ?? [];

  const schemaByName = useMemo(
    () => new Map(schema.columns.map((c) => [c.name, c])),
    [schema.columns],
  );

  const columns = useMemo(
    () =>
      debouncedColumnNames.map((name) => {
        const col = schemaByName.get(name);
        return { key: name, feature: col ? cellFeatureFor(col) : undefined };
      }),
    [debouncedColumnNames, schemaByName],
  );

  const totalRows = data?.num_rows ?? schema.num_rows;
  const hasPrev = offset > 0;
  const hasNext = offset + limit < totalRows;

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
                  <span className="tabular-nums text-fg-subtle">
                    {t("parquet.viewer.pageInfo", {
                      from: totalRows === 0 ? 0 : offset + 1,
                      to: Math.min(offset + limit, totalRows),
                      total: formatNumber(totalRows),
                    })}
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
                />
              ) : columns.length === 0 ? (
                <EmptyState
                  icon={Database}
                  title={t("parquet.viewer.noColumnsTitle")}
                  description={t("parquet.viewer.noColumnsDescription")}
                />
              ) : rows.length === 0 && !isFetching ? (
                <EmptyState icon={Database} title={t("parquet.viewer.noRowsTitle")} />
              ) : (
                <DataTable columns={columns} rows={rows} />
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
}
