"use client";

import type { ElementContent } from "hast";
import { FileText } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { EmptyState } from "@/components/ui/empty-state";
import { cn } from "@/lib/cn";
import {
  type CodeHighlightResult,
  type CodeLine,
  MAX_HIGHLIGHT_LINES,
  plainCodeLines,
} from "@/lib/code-lines";
import { formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";

/**
 * Renders the hast a highlighted line is made of.
 *
 * `rehype-highlight` only ever produces `<span class="hljs-*">` elements and
 * text, so one recursive function covers the whole tree; anything else is
 * dropped rather than trusted. Index keys are correct here because the list is
 * derived from immutable source text and never reordered.
 */
function renderNodes(nodes: ElementContent[]): React.ReactNode {
  return nodes.map((node, index) => {
    if (node.type === "text") return node.value;
    if (node.type !== "element") return null;
    const className = node.properties?.className;
    const classes = Array.isArray(className)
      ? className.map(String).join(" ")
      : typeof className === "string"
        ? className
        : undefined;
    return (
      // biome-ignore lint/suspicious/noArrayIndexKey: positional nodes in immutable source text
      <span key={index} className={classes}>
        {renderNodes(node.children)}
      </span>
    );
  });
}

/** Reads `#L12` off the URL and keeps following it as the reader clicks around. */
function useLineTarget(): number | null {
  const [target, setTarget] = useState<number | null>(null);
  useEffect(() => {
    const read = () => {
      const match = /^#L(\d+)$/.exec(window.location.hash);
      setTarget(match ? Number(match[1]) : null);
    };
    read();
    window.addEventListener("hashchange", read);
    return () => window.removeEventListener("hashchange", read);
  }, []);
  return target;
}

function CodeLines({ lines }: { lines: CodeLine[] }) {
  const t = useT();
  const target = useLineTarget();

  // Every gutter cell is sized for the widest number in the file, or the rule
  // between the numbers and the code would zig-zag where the line count gains
  // a digit. `ch` is exact here: the gutter inherits `font-mono`, and the
  // digits are `tabular-nums`. The 1.5rem is the cell's own `px-3`.
  const gutterWidth = `calc(${String(lines.length).length}ch + 1.5rem)`;

  return (
    // `.tf-hljs` is the scope the highlight.js token colours are declared
    // under in globals.css — the same class the Markdown wrapper carries, so a
    // fence in a README and a source file are coloured by one stylesheet.
    <pre className="tf-hljs scroll-x max-h-[75vh] overflow-y-auto py-3 text-xs leading-relaxed">
      <code className="block font-mono">
        {lines.map((line) => (
          <span
            key={line.number}
            id={`L${line.number}`}
            className={cn(
              // `w-max min-w-full` so the row keeps its background across the
              // full width for a short line and still grows past the viewport
              // for a long one, which is what makes the gutter stick.
              "flex w-max min-w-full",
              target === line.number ? "bg-accent-muted" : "bg-bg-raised",
            )}
          >
            <a
              href={`#L${line.number}`}
              aria-label={t("repo.codePreview.lineLink", { line: line.number })}
              style={{ width: gutterWidth }}
              className="sticky left-0 shrink-0 select-none border-r border-border bg-inherit px-3 text-right font-medium tabular-nums text-fg-subtle hover:text-accent"
            >
              {line.number}
            </a>
            <span className="shrink-0 whitespace-pre px-3">{renderNodes(line.nodes)}</span>
          </span>
        ))}
      </code>
    </pre>
  );
}

/**
 * Source / plain-text rendering of a file preview, plus the notice that says
 * the server cut it short.
 *
 * Split out of file-preview.tsx because the CSV/JSONL table view needs the
 * same block as its "Raw" mode and as the fallback when a file turns out not to
 * parse as a table, and the Markdown preview needs it for its Raw mode. Client
 * component so `useT` works from every one of those call sites.
 *
 * Every file gets a line-number gutter whose numbers are `#L12` deep links.
 * Token colours come from `highlighted`, which the Server Component that owns
 * the file builds with `lib/code-highlight.ts`; when it is absent (a client
 * caller whose text only exists in the browser) the same gutter is rendered
 * over plain text. Files past the caps in `lib/code-lines.ts` fall back to a
 * flat `<pre>` and say why.
 */
export function TextPreview({
  content,
  truncated,
  downloadUrl,
  highlighted: precomputed,
}: {
  content: string;
  /** True when the *server* clipped the preview at its 512KB limit. */
  truncated?: boolean;
  downloadUrl: string;
  /**
   * The highlighted lines, already built. Passing this in is what keeps
   * `rehype-highlight` and lowlight's 37 grammars out of the client bundle:
   * the Server Component that owns the file calls `highlightSource` and hands
   * the result down, so the browser downloads and re-runs none of it. Callers
   * that are themselves client components (the tabular preview's Raw mode)
   * omit it and fall back to building here, where the cost is real but the
   * input is a CSV the user already chose to view as text.
   */
  highlighted?: CodeHighlightResult;
}) {
  const t = useT();
  const built = useMemo(() => precomputed ?? plainCodeLines(content), [precomputed, content]);

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
      {built.ok ? (
        <CodeLines lines={built.lines} />
      ) : (
        <>
          {/* Above the text on purpose: it explains what the reader is about
              to look at, and it is a static condition, so nothing moves. */}
          <div className="border-b border-border px-4 py-2 text-xs font-medium text-fg-subtle">
            {built.reason === "tooManyLines"
              ? t("repo.codePreview.tooManyLines", {
                  lines: formatNumber(built.lines),
                  limit: formatNumber(MAX_HIGHLIGHT_LINES),
                })
              : t("repo.codePreview.tooLarge")}
          </div>
          <pre className="scroll-x max-h-[75vh] overflow-y-auto p-4 text-xs leading-relaxed">
            <code className="font-mono">{content}</code>
          </pre>
        </>
      )}
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
