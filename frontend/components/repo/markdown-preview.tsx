"use client";

import { Code, Eye } from "lucide-react";
import { useState } from "react";
import { TextPreview } from "@/components/repo/text-preview";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { useT } from "@/lib/i18n/client";

type Mode = "rendered" | "raw";

/**
 * Markdown file preview with the Rendered / Raw switch the docs promise.
 *
 * Only the mode lives here. The rendered half arrives as an already-rendered
 * element from the Server Component that owns the file
 * (`components/repo/file-preview.tsx`), which keeps `react-markdown` and its
 * plugin chain out of the client bundle — the blob page pays no JavaScript for
 * a toggle. The raw half reuses `TextPreview`, so the source is highlighted and
 * line-numbered like any other file instead of being a second, flatter text
 * renderer.
 *
 * The control sits above the panel and stays put when the mode changes
 * (DESIGN.md §8).
 */
export function MarkdownPreview({
  source,
  truncated,
  downloadUrl,
  rendered,
  highlighted,
}: {
  /** The Markdown source, shown as-is in Raw mode. */
  source: string;
  /** True when the server clipped the preview at its 512KB limit. */
  truncated?: boolean;
  downloadUrl: string;
  /** The rendered Markdown panel, built on the server. */
  rendered: React.ReactNode;
  /**
   * The Raw pane's highlighted source, also built on the server. Same reason
   * as `rendered`: a toggle should cost no parser in the browser.
   */
  highlighted?: React.ComponentProps<typeof TextPreview>["highlighted"];
}) {
  const t = useT();
  const [mode, setMode] = useState<Mode>("rendered");

  const modes = [
    { value: "rendered" as const, label: t("repo.markdownPreview.modeRendered"), icon: Eye },
    { value: "raw" as const, label: t("repo.markdownPreview.modeRaw"), icon: Code },
  ];

  return (
    <div className="flex flex-col gap-3">
      <SegmentedControl
        label={t("repo.markdownPreview.previewMode")}
        value={mode}
        options={modes}
        onChange={setMode}
      />
      {mode === "raw" ? (
        <TextPreview
          content={source}
          truncated={truncated}
          downloadUrl={downloadUrl}
          highlighted={highlighted}
        />
      ) : (
        rendered
      )}
    </div>
  );
}
