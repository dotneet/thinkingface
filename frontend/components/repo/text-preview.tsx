"use client";

import { FileText } from "lucide-react";
import { EmptyState } from "@/components/ui/empty-state";
import { useT } from "@/lib/i18n/client";

/**
 * Plain-text rendering of a file preview, plus the notice that says the server
 * cut it short.
 *
 * Split out of file-preview.tsx because the CSV/JSONL table view needs the
 * same block as its "Raw" mode and as the fallback when a file turns out not to
 * parse as a table. Client component so `useT` works from both the server-side
 * file preview and the client-side tabular preview.
 */
export function TextPreview({
  content,
  truncated,
  downloadUrl,
}: {
  content: string;
  /** True when the *server* clipped the preview at its 512KB limit. */
  truncated?: boolean;
  downloadUrl: string;
}) {
  const t = useT();

  // A zero-byte file otherwise renders as an empty bordered box, which reads
  // as a failed load rather than as "there is nothing in here". The tabular
  // and README previews already say so out loud; this matches them.
  if (content === "") {
    return (
      <div className="rounded-lg border border-border bg-bg-raised">
        <EmptyState icon={FileText} title={t("repo.preview.emptyFile")} />
      </div>
    );
  }

  return (
    <div className="rounded-lg border border-border bg-bg-raised">
      <pre className="scroll-x max-h-[75vh] overflow-y-auto p-4 text-xs leading-relaxed">
        <code className="font-mono">{content}</code>
      </pre>
      {truncated && <TruncatedNotice downloadUrl={downloadUrl} />}
    </div>
  );
}

/** Footer for any preview the server clipped (text, markdown, …). */
export function TruncatedNotice({ downloadUrl }: { downloadUrl: string }) {
  const t = useT();
  // Split the translation template on its placeholder so only the {link} part renders as an anchor.
  const [before = "", after = ""] = t("repo.preview.truncatedNotice").split("{link}");
  return (
    <div className="border-t border-border px-4 py-2 text-xs font-medium text-fg-subtle">
      {before}
      <a href={downloadUrl} className="text-accent hover:underline">
        {t("repo.preview.truncatedNoticeLink")}
      </a>
      {after}
    </div>
  );
}
