"use client";

import { Link2 } from "lucide-react";
import { useT } from "@/lib/i18n/client";

/**
 * The permalink `rehype-autolink-headings` appends to every heading.
 *
 * It exists as its own client component for one reason: the accessible name
 * ("Permalink: Installation") is user-visible text and therefore has to come
 * from the dictionary, while `<Markdown>` itself stays neutral so async Server
 * Components can render it. Only the leaf that needs a translator opts in.
 *
 * The icon is decorative — `aria-label` already names the link — so it is
 * hidden from assistive technology. Hover/focus visibility is `.tf-markdown`'s
 * business, not this component's.
 */
export function MarkdownHeadingAnchor({
  href,
  heading,
}: {
  href?: string;
  /** Text of the heading this anchor belongs to, for the accessible name. */
  heading?: string;
}) {
  const t = useT();
  return (
    <a
      href={href}
      className="tf-heading-anchor"
      aria-label={t("ui.markdown.headingAnchor", { heading: heading ?? "" })}
    >
      <Link2 size={14} aria-hidden />
    </a>
  );
}
