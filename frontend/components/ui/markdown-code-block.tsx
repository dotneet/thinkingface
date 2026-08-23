"use client";

import { useRef } from "react";
import { CopyButton } from "@/components/ui/copy-button";
import { useT } from "@/lib/i18n/client";

/**
 * A fenced code block inside rendered Markdown: the language it was tagged
 * with, a copy button, and the highlighted `<pre><code>` itself.
 *
 * The layout deliberately mirrors `ui/code-block.tsx`'s labelled variant (an
 * uppercase metadata row with an icon-only copy button on its right) so a
 * snippet in a README and a snippet in a clone panel look like the same thing.
 * What it cannot reuse is the primitive itself: the text shown here is the
 * highlighted DOM `rehype-highlight` produced, not a string we hold.
 *
 * Hence the ref: the copy thunk reads `textContent` off the rendered `<pre>`,
 * which is the source itself minus the `<span class="hljs-*">` wrappers, and
 * stays correct however the highlighter chooses to split the tokens.
 *
 * `tf-codeblock` / `tf-codeblock-bar` are the hooks `.tf-markdown`'s stylesheet
 * uses; the `hljs-*` token colours live there too.
 */
export function MarkdownCodeBlock({
  language,
  children,
}: {
  /** Language from the fence's `language-*` class, if it had one. */
  language?: string;
  children?: React.ReactNode;
}) {
  const t = useT();
  const preRef = useRef<HTMLPreElement>(null);

  return (
    <div className="tf-codeblock flex flex-col gap-1.5">
      <div className="tf-codeblock-bar flex items-center justify-between gap-2">
        <span className="text-xs font-medium uppercase tracking-wide text-fg-subtle">
          {language ?? ""}
        </span>
        <CopyButton
          value={() => preRef.current?.textContent ?? ""}
          label={t("ui.markdown.copyCode")}
          iconOnly
        />
      </div>
      <pre ref={preRef}>{children}</pre>
    </div>
  );
}
