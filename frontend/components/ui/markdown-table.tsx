"use client";

import { useT } from "@/lib/i18n/client";

/**
 * A Markdown table in a horizontally scrollable region.
 *
 * A wide comparison table is common in model cards and would otherwise push
 * the whole page sideways. `<section>` (rather than a `div` with
 * `role="region"`) plus an accessible name is what makes the scroll area show
 * up as a landmark; an unnamed region is ignored by screen readers, and the
 * name is user-visible text, which is why this needs a translator and
 * therefore lives in its own client leaf.
 */
export function MarkdownTable({ children }: { children?: React.ReactNode }) {
  const t = useT();
  return (
    <section
      className="tf-table-wrap scroll-x"
      // Keyboard users must be able to scroll the overflow; without a tab stop
      // the only way to reach the right-hand columns is a pointer.
      // biome-ignore lint/a11y/noNoninteractiveTabindex: scrollable region needs a tab stop
      tabIndex={0}
      aria-label={t("ui.markdown.table")}
    >
      <table>{children}</table>
    </section>
  );
}
