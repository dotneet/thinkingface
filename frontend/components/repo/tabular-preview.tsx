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

/**
 * Why the full-file download failed. `status: null` is a network-level failure
 * (offline, DNS, CORS, abort) — the only thing `fetch` itself rejects for.
 */
type FetchFailure = { status: number | null };

/**
 * Turns a failure of the raw `fetch` below into translated copy.
 *
 * This is not an `apiFetch` call (the body is a whole file, not JSON), so
 * `errorMessage()` cannot be applied to it — but the same rule holds: nothing
 * the backend or the browser wrote in English may reach the screen inside a
 * translated sentence (DESIGN.md §7). The status *line* ("404 Not Found") and
 * the browser's own message ("Failed to fetch") are therefore dropped; a
 * status this maps gets a translated phrase, and anything else keeps only the
 * status number, which is the one genuinely useful, language-neutral detail.
 */
function fetchFailureReason(t: Translator, failure: FetchFailure): string {
  const { status } = failure;
  if (status === null) return t("repo.tabular.networkError");
  if (status === 401) return t("repo.tabular.fetchReasonUnauthorized");
  if (status === 403) return t("repo.tabular.fetchReasonForbidden");
  if (status === 404) return t("repo.tabular.fetchReasonNotFound");
  if (status >= 500) return t("repo.tabular.fetchReasonServer");
  return t("repo.tabular.fetchReasonStatus", { status: String(status) });
}

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
 * only fetches the whole file (browser → API, uncredentialed, as the fetch
 * below explains) when the preview was clipped. Anything that fails to
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
  const [fetchFailure, setFetchFailure] = useState<FetchFailure | null>(null);

  useEffect(() => {
    if (!previewTruncated) return;
    let cancelled = false;
    setFetching(true);
    // A failure belongs to one download; carrying it into the next file's
    // fetch would report an error that is not this file's.
    setFetchFailure(null);
    (async () => {
      try {
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
        const res = await fetch(downloadUrl, { credentials: "omit" });
        if (!res.ok) {
          // Only the status code is kept; the status line is server-authored
          // English and is translated at display time (fetchFailureReason).
          if (!cancelled) setFetchFailure({ status: res.status });
          return;
        }
        const text = await res.text();
        if (!cancelled) setFullText(text);
      } catch {
        // `fetch` rejects only below HTTP: offline, DNS, CORS, abort. The
        // browser's own message for those is untranslated and says nothing
        // the network-error copy doesn't, so it is not carried forward.
        if (!cancelled) setFetchFailure({ status: null });
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

      {fetchFailure !== null && (
        <Alert tone="warning" title={t("repo.tabular.fetchFailedTitle")}>
          {t("repo.tabular.fetchFailedBody", { error: fetchFailureReason(t, fetchFailure) })}
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
