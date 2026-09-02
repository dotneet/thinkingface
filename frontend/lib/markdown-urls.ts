import { defaultUrlTransform } from "react-markdown";
import { HEADING_ID_PREFIX } from "@/lib/markdown-pipeline";
import { decodeSegment, repoBlobHref, repoTreeHref } from "@/lib/paths";
import type { RepoKind } from "@/types/api";

/**
 * Where a Markdown document lives inside a repository, so relative links to
 * other files can be turned into in-app blob / tree links instead of raw
 * downloads.
 */
export type MarkdownLinkContext = {
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
  /** Directory of the Markdown file itself, as decoded path segments (root = []). */
  dir: string[];
};

/** A URL we must not touch: in-page anchor, protocol-relative, or absolute. */
function isNonRelative(url: string): boolean {
  return url.startsWith("#") || url.startsWith("//") || /^[a-z][a-z0-9+.-]*:/i.test(url);
}

/**
 * Apply `.` / `..` / empty segments of `path` to `base`, returning decoded
 * segments. `..` at the root is dropped rather than escaping the repository.
 *
 * Decoding happens **before** the `.` / `..` test, not after. Testing the raw
 * segment first let `%2e%2e` through as an ordinary directory name, so
 * `%2e%2e/%2e%2e/secret.png` produced `…/blob/main/docs/../../secret.png` —
 * a link the app believed pointed inside the repository and the browser
 * normalized straight back out of it (`[go](%2e%2e/%2e%2e/%2e%2e/settings/tokens)`
 * in a README lands on the settings page, and the `![img]` spelling fires the
 * same-origin GET without a click).
 *
 * A decoded segment can itself contain a separator (`%2f` → `/`), and
 * `repoBlobHref` re-splits on those downstream, so the decoded value is fed
 * back through the same split instead of being pushed whole — otherwise
 * `%2f..%2f` would smuggle a `..` past this loop.
 */
function normalizeSegments(base: string[], path: string): string[] {
  const out = path.startsWith("/") ? [] : [...base];
  for (const raw of path.split("/")) {
    for (const segment of decodeSegment(raw).split("/")) {
      if (segment === "" || segment === ".") continue;
      if (segment === "..") {
        out.pop();
        continue;
      }
      out.push(segment);
    }
  }
  return out;
}

/**
 * Split the repository root and the document's directory back out of the two
 * resolve URLs a call site hands us, so `../img/a.png` can walk up without
 * escaping the repository. Segments come back decoded, because they are
 * re-encoded on the way out.
 */
function assetBaseSegments(
  assetBaseUrl: string,
  repoRootUrl?: string,
): { root: string; dir: string[] } {
  const trim = (s: string) => s.replace(/\/+$/, "");
  const base = trim(assetBaseUrl);
  const root = trim(repoRootUrl ?? assetBaseUrl);
  const dir = base.startsWith(root)
    ? base
        .slice(root.length)
        .split("/")
        .filter((s) => s.length > 0)
        .map(decodeSegment)
    : [];
  return { root, dir };
}

/**
 * Resolve a Markdown *asset* URL (`src`, `poster`) against the repository's
 * resolve endpoint so relative images like `./plot.png` actually load.
 */
export function markdownUrlTransform(
  url: string,
  assetBaseUrl?: string,
  repoRootUrl?: string,
): string {
  if (!assetBaseUrl) return defaultUrlTransform(url);
  if (isNonRelative(url)) return defaultUrlTransform(url);

  // `plot.png?v=2` must stay a path plus a query, not become one encoded
  // segment — the same split `markdownHrefTransform` does.
  const { pathPart, suffix } = splitSuffix(url);
  const { root, dir } = assetBaseSegments(assetBaseUrl, repoRootUrl);
  const segments = normalizeSegments(pathPart.startsWith("/") ? [] : dir, pathPart);
  return `${root}/${segments.map(encodeURIComponent).join("/")}${suffix}`;
}

/** Separate `?query` / `#fragment` from the path so they survive re-encoding. */
function splitSuffix(url: string): { pathPart: string; suffix: string } {
  const split = /^([^?#]*)([?#].*)?$/.exec(url);
  return { pathPart: split?.[1] ?? "", suffix: split?.[2] ?? "" };
}

/**
 * Resolve a Markdown *link* URL (`href`).
 *
 * Without a {@link MarkdownLinkContext} a relative link is left alone — the
 * one thing it must never become is a resolve URL, which turns
 * `[docs](docs/usage.md)` into a raw-text download instead of a page.
 *
 * With one, a relative or root-relative link becomes an in-app route: a blob
 * page for a file, a tree page for anything ending in `/`. Fragments, query
 * strings, absolute URLs and `mailto:` are passed through untouched.
 */
export function markdownHrefTransform(url: string, ctx?: MarkdownLinkContext): string {
  const safe = defaultUrlTransform(url);
  // An in-document link (`[see](#installation)`): the heading it points at
  // carries a namespaced id, so the fragment has to as well. This applies
  // with or without a link context — the ids are ours either way.
  if (safe.startsWith("#")) return prefixFragment(safe);
  if (!ctx || safe === "" || isNonRelative(safe)) return safe;

  // Keep `?query` / `#fragment` attached to whatever route we build. The
  // route is another Markdown file rendered by the same pipeline, so its
  // headings are namespaced too: `usage.md#install` → `…/usage.md#user-content-install`.
  const { pathPart, suffix } = splitSuffix(safe);
  if (pathPart === "") return safe;

  const segments = normalizeSegments(pathPart.startsWith("/") ? [] : ctx.dir, pathPart);
  const path = segments.join("/");
  const { kind, ns, name, rev } = ctx;
  if (path === "" || pathPart.endsWith("/")) {
    return repoTreeHref(kind, ns, name, rev, path) + prefixFragment(suffix);
  }
  return repoBlobHref(kind, ns, name, rev, path) + prefixFragment(suffix);
}

/**
 * Ids the Markdown pipeline writes *without* {@link HEADING_ID_PREFIX}: the
 * GFM footnotes heading keeps its fixed `footnote-label` id (allowlisted as-is
 * in `lib/markdown-sanitize.ts`), so a link to it must not be rewritten.
 */
const UNPREFIXED_IDS = new Set(["footnote-label"]);

/**
 * Re-point the `#fragment` of `suffix` (which may also carry a `?query`
 * before it) at the namespaced id the pipeline gives headings. Fragments that
 * are already namespaced — footnote links arrive that way from
 * `mdast-util-to-hast`, and so do the heading permalinks — are left alone.
 */
export function prefixFragment(suffix: string): string {
  const hash = suffix.indexOf("#");
  if (hash < 0) return suffix;
  const fragment = suffix.slice(hash + 1);
  if (fragment === "" || fragment.startsWith(HEADING_ID_PREFIX) || UNPREFIXED_IDS.has(fragment)) {
    return suffix;
  }
  return `${suffix.slice(0, hash + 1)}${HEADING_ID_PREFIX}${fragment}`;
}

export type MarkdownUrlOptions = {
  assetBaseUrl?: string;
  repoRootUrl?: string;
  linkContext?: MarkdownLinkContext;
};

/**
 * Build react-markdown's `urlTransform` for one document. The `key` argument
 * is the attribute name, which is the only thing separating an asset (`src`,
 * `poster`) from a link (`href`) — they resolve to different places.
 */
export function makeMarkdownUrlTransform(
  opts: MarkdownUrlOptions,
): (url: string, key: string) => string {
  return (url, key) =>
    key === "href"
      ? markdownHrefTransform(url, opts.linkContext)
      : markdownUrlTransform(url, opts.assetBaseUrl, opts.repoRootUrl);
}

/** Does this (already transformed) href leave the app? */
export function isExternalHref(href: string | undefined): boolean {
  return href !== undefined && /^(https?:)?\/\//i.test(href);
}
