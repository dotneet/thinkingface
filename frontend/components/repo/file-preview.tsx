import { ChartNoAxesCombined, Download, FileImage } from "lucide-react";
import Link from "next/link";
import { ModelInspector } from "@/components/model/model-inspector";
import { MarkdownPreview } from "@/components/repo/markdown-preview";
import { TabularPreview } from "@/components/repo/tabular-preview";
import { TextPreview, TruncatedNotice } from "@/components/repo/text-preview";
import { buttonClass } from "@/components/ui/button";
import { CodeBlock } from "@/components/ui/code-block";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Markdown, type MarkdownLinkContext } from "@/components/ui/markdown";
import { errorMessage, type FailedApiResult } from "@/lib/api-error-message";
import { formatBytes } from "@/lib/format";
import { getT } from "@/lib/i18n/server";
import { decodeRawContent } from "@/lib/raw-content";
import { MAX_TABULAR_BYTES, tabularFormatFor } from "@/lib/tabular";
import type { RawFileResponse, RepoKind, TreeEntryUI } from "@/types/api";

// Identifies the blob so ModelInspector can fetch /api/v1/model-meta itself.
// Optional so a caller that has no revision context still renders the plain
// "no preview" state instead of failing to compile.
export type ModelPreviewSource = {
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
  path: string[];
};

export async function FilePreview({
  entry,
  raw,
  rawError,
  downloadUrl,
  viewerHref,
  assetBaseUrl,
  repoRootUrl,
  linkContext,
  modelSource,
}: {
  entry: Pick<TreeEntryUI, "preview" | "name" | "size" | "gcloud_command">;
  raw: RawFileResponse | null;
  /**
   * Why `raw` is missing, when it is missing because the fetch failed. Without
   * it a previewable file whose contents could not be read is indistinguishable
   * from a file that has no preview, and the reader is told the wrong thing.
   * Carries the full failed `ApiResult` (not just its `.message`) so this can
   * render a translated message via `errorMessage()` instead of the backend's
   * raw English text ([S12]).
   */
  rawError?: FailedApiResult | null;
  downloadUrl: string;
  viewerHref?: string;
  assetBaseUrl?: string;
  repoRootUrl?: string;
  /** Lets relative links in a previewed Markdown file resolve to blob / tree pages. */
  linkContext?: MarkdownLinkContext;
  modelSource?: ModelPreviewSource;
}) {
  const t = await getT();

  const gcsBlock = entry.gcloud_command ? (
    <div className="w-full max-w-md text-left">
      <CodeBlock
        value={entry.gcloud_command}
        label={t("repo.preview.gcsCommandLabel")}
        copyLabel={t("repo.preview.gcsCopyCommand")}
      />
    </div>
  ) : null;

  if (entry.preview === "image") {
    return (
      <div className="flex flex-col items-center gap-3 rounded-lg border border-border bg-bg-sunken p-6">
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img src={downloadUrl} alt={entry.name} className="max-h-[70vh] max-w-full rounded-md" />
        <a
          href={downloadUrl}
          className="flex items-center gap-1.5 text-sm text-accent hover:underline"
        >
          <Download size={14} />
          {t("repo.preview.downloadOriginal")}
        </a>
        {gcsBlock}
      </div>
    );
  }

  if (entry.preview === "parquet") {
    return (
      <EmptyState
        icon={ChartNoAxesCombined}
        title={t("repo.preview.parquetTitle")}
        description={t("repo.preview.parquetDescription", { size: formatBytes(entry.size) })}
        action={
          <div className="flex flex-col items-center gap-3">
            <div className="flex flex-wrap items-center justify-center gap-2">
              {viewerHref && (
                <Link href={viewerHref} className={buttonClass({ variant: "primary" })}>
                  <ChartNoAxesCombined size={14} />
                  {t("repo.preview.openInViewer")}
                </Link>
              )}
              <a href={downloadUrl} className={buttonClass()}>
                <Download size={14} />
                {t("repo.preview.download")}
              </a>
            </div>
            {gcsBlock}
          </div>
        }
      />
    );
  }

  if (entry.preview === "model" && modelSource) {
    return (
      <ModelInspector
        kind={modelSource.kind}
        ns={modelSource.ns}
        name={modelSource.name}
        rev={modelSource.rev}
        path={modelSource.path}
        size={entry.size}
        downloadUrl={downloadUrl}
      />
    );
  }

  const downloadAction = (
    <div className="flex flex-col items-center gap-3">
      <a href={downloadUrl} className={buttonClass({ variant: "primary" })}>
        <Download size={14} />
        {t("repo.preview.download")}
      </a>
      {gcsBlock}
    </div>
  );

  // A file we *would* preview but whose contents didn't arrive is a failure,
  // not an absence — say so instead of claiming the type can't be previewed.
  if (rawError) {
    return (
      <ErrorState
        title={t("repo.preview.loadErrorTitle")}
        message={errorMessage(t, rawError)}
        hint={t("repo.preview.loadErrorHint")}
        action={downloadAction}
      />
    );
  }

  if (entry.preview === "binary" || entry.preview === "model" || !raw) {
    return (
      <EmptyState
        icon={FileImage}
        title={t("repo.preview.noPreviewTitle")}
        description={t("repo.preview.noPreviewDescription", { size: formatBytes(entry.size) })}
        action={downloadAction}
      />
    );
  }

  const content = decodeRawContent(raw);
  if (content === null) {
    return (
      <ErrorState
        title={t("repo.preview.decodeErrorTitle")}
        message={t("repo.preview.decodeErrorMessage")}
        action={downloadAction}
      />
    );
  }

  if (entry.preview === "markdown") {
    return (
      <MarkdownPreview
        source={content}
        truncated={raw.truncated}
        downloadUrl={downloadUrl}
        fileName={entry.name}
        // Rendered here rather than inside the client wrapper so react-markdown
        // and its plugin chain stay on the server; the toggle only chooses
        // which of the two already-built panels is on screen.
        rendered={
          <div className="rounded-lg border border-border bg-bg-raised p-6">
            <Markdown
              source={content}
              assetBaseUrl={assetBaseUrl}
              repoRootUrl={repoRootUrl}
              linkContext={linkContext}
              // Without this a README opened from the file tree starts with the
              // card's `---\nlicense: mit\n---`, which CommonMark reads as a
              // thematic break plus a setext heading rather than metadata.
              stripFrontmatter
            />
            {raw.truncated && <TruncatedNotice downloadUrl={downloadUrl} />}
          </div>
        }
      />
    );
  }

  // .csv / .tsv / .jsonl arrive here as plain text (they are PreviewKindText on
  // the backend); render them as a real table when they are small enough to
  // parse in the browser, and let TabularPreview fall back to this same text
  // view when the contents turn out not to be tabular after all.
  const tabularFormat = tabularFormatFor(entry.name);
  if (tabularFormat && entry.size <= MAX_TABULAR_BYTES) {
    return (
      <TabularPreview
        format={tabularFormat}
        previewText={content}
        previewTruncated={raw.truncated}
        size={entry.size}
        downloadUrl={downloadUrl}
      />
    );
  }

  return (
    <TextPreview
      content={content}
      truncated={raw.truncated}
      downloadUrl={downloadUrl}
      // Drives the syntax highlighting: the extension (or a well-known
      // extensionless name) is the only signal used, never the contents.
      fileName={entry.name}
    />
  );
}
