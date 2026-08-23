"use client";

import { FileText, Table2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { TextPreview } from "@/components/repo/text-preview";
import { Alert } from "@/components/ui/alert";
import { DataTable } from "@/components/ui/data-table";
import { EmptyState } from "@/components/ui/empty-state";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { Skeleton } from "@/components/ui/skeleton";
import { formatBytes, formatNumber } from "@/lib/format";
import type { Translator } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import { MAX_ROWS, parseTabular, type TabularFormat, type TabularParseError } from "@/lib/tabular";

/** Turns parseTabular's reason code into a translated clause. */
function parseReason(t: Translator, error: TabularParseError): string {
  switch (error.reason) {
    case "tooManyColumns":
      return t("repo.tabular.parseTooManyColumns", { columns: String(error.columns) });
    case "raggedRows":
      return t("repo.tabular.parseRaggedRows");
    case "noJsonObjects":
      return t("repo.tabular.parseNoJsonObjects");
    case "tooManyInvalidLines":
      return t("repo.tabular.parseTooManyInvalidLines");
    default:
      return t("repo.tabular.parseNoRows");
  }
}

type Mode = "table" | "raw";

/**
 * Table view for .csv / .tsv / .jsonl blobs.
 *
 * The backend classifies these as plain text and caps the preview it returns at
 * 512KB, so this component parses that preview directly when it is complete and
 * only fetches the whole file (browser → API, with credentials, same as any
 * other resolve download) when the preview was clipped. Anything that fails to
 * parse — or is simply too big — degrades to the text preview rather than
 * showing a half-guessed grid.
 */
export function TabularPreview({
  format,
  previewText,
  previewTruncated,
  size,
  downloadUrl,
}: {
  format: TabularFormat;
  /** The (possibly truncated) preview the server already returned. */
  previewText: string;
  previewTruncated: boolean;
  size: number;
  downloadUrl: string;
}) {
  const t = useT();
  const [mode, setMode] = useState<Mode>("table");
  const [fullText, setFullText] = useState<string | null>(null);
  const [fetching, setFetching] = useState(previewTruncated);
  const [fetchError, setFetchError] = useState<string | null>(null);

  useEffect(() => {
    if (!previewTruncated) return;
    let cancelled = false;
    setFetching(true);
    (async () => {
      try {
        // `credentials: "include"` sends tf_session so the request is authenticated; the
        // backend echoes the Origin and allows credentials (`cors` in
        // backend/internal/api/server.go).
        const res = await fetch(downloadUrl, { credentials: "include" });
        if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
        const text = await res.text();
        if (!cancelled) setFullText(text);
      } catch (err) {
        // An empty string marks an "unspecified network error"; translated at display time.
        if (!cancelled) setFetchError(err instanceof Error ? err.message : "");
      } finally {
        if (!cancelled) setFetching(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [downloadUrl, previewTruncated]);

  // Falls back to the clipped preview when the full download failed, so a
  // flaky network costs rows rather than the whole table.
  const text = fullText ?? previewText;
  const stillTruncated = previewTruncated && fullText === null;
  const parsed = useMemo(() => parseTabular(text, format), [text, format]);

  if (fetching) {
    return (
      <div className="flex flex-col gap-3">
        <Skeleton className="h-8 w-40" />
        <Skeleton className="h-96 w-full" />
      </div>
    );
  }

  const rawView = (
    <TextPreview content={text} truncated={stillTruncated} downloadUrl={downloadUrl} />
  );

  if (!parsed.ok) {
    return (
      <div className="flex flex-col gap-3">
        <Alert tone="info" title={t("repo.tabular.rawFallbackTitle")}>
          {t("repo.tabular.rawFallbackBody", { message: parseReason(t, parsed) })}
        </Alert>
        {rawView}
      </div>
    );
  }

  const { columns, rows, malformed, truncated } = parsed.table;

  const modes = [
    { value: "table" as const, label: t("repo.tabular.modeTable"), icon: Table2 },
    { value: "raw" as const, label: t("repo.tabular.modeRaw"), icon: FileText },
  ];

  // Split the template so only {link} (download the whole file) renders as an anchor.
  const [rowLimitBefore = "", rowLimitAfter = ""] = t("repo.tabular.rowLimit", {
    rows: formatNumber(MAX_ROWS),
  }).split("{link}");

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-3">
        <SegmentedControl
          label={t("repo.tabular.previewMode")}
          value={mode}
          options={modes}
          onChange={setMode}
        />
        <span className="text-xs font-medium tabular-nums text-fg-subtle">
          {t("repo.tabular.stats", {
            rows: formatNumber(rows.length),
            columns: formatNumber(columns.length),
            size: formatBytes(size),
          })}
        </span>
      </div>

      {fetchError !== null && (
        <Alert tone="warning" title={t("repo.tabular.fetchFailedTitle")}>
          {t("repo.tabular.fetchFailedBody", {
            error: fetchError || t("repo.tabular.networkError"),
          })}
        </Alert>
      )}

      {truncated && (
        <Alert tone="warning">
          {rowLimitBefore}
          <a href={downloadUrl} className="text-accent hover:underline">
            {t("repo.tabular.rowLimitLink")}
          </a>
          {rowLimitAfter}
        </Alert>
      )}

      {malformed > 0 && (
        <Alert tone="warning">
          {format === "jsonl"
            ? t(
                malformed === 1
                  ? "repo.tabular.malformedJsonlOne"
                  : "repo.tabular.malformedJsonlOther",
                {
                  count: formatNumber(malformed),
                },
              )
            : t(
                malformed === 1 ? "repo.tabular.malformedCsvOne" : "repo.tabular.malformedCsvOther",
                {
                  count: formatNumber(malformed),
                },
              )}{" "}
          {t("repo.tabular.switchToRaw")}
        </Alert>
      )}

      {mode === "raw" ? (
        rawView
      ) : rows.length === 0 ? (
        <EmptyState
          icon={Table2}
          title={t("repo.tabular.emptyTitle")}
          description={t("repo.tabular.emptyDescription")}
        />
      ) : (
        <DataTable columns={columns.map((key) => ({ key }))} rows={rows} className="max-h-[75vh]" />
      )}
    </div>
  );
}
