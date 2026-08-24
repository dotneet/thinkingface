import type { Metadata } from "next";

/**
 * The brand. Never translated and never abbreviated (DESIGN.md §7), so it is
 * written here rather than pulled from a dictionary.
 */
const SITE_NAME = "🤔 Thinking Face";

/** What separates the parts of a title, in every locale. */
const SEPARATOR = " · ";

/**
 * Builds a page `<title>`: the given parts, broadest first, with the brand
 * last — `admin/imdb-reviews · Files · 🤔 Thinking Face`.
 *
 * Empty and `undefined` parts are dropped, so a caller can pass an optional
 * segment (a file path that may be the repository root, say) inline without a
 * conditional. Identifiers — namespaces, repository and project names, file
 * paths — go in verbatim; only the words describing them come from
 * `meta.*` in the dictionaries.
 */
export function pageTitle(...parts: (string | undefined | null | false)[]): string {
  return [...parts.filter((part): part is string => Boolean(part)), SITE_NAME].join(SEPARATOR);
}

/**
 * {@link pageTitle} wrapped in the `Metadata` object a `generateMetadata`
 * returns, which is all most routes need.
 */
export function titleMetadata(...parts: (string | undefined | null | false)[]): Metadata {
  return { title: pageTitle(...parts) };
}
