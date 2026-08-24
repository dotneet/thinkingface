import GithubSlugger from "github-slugger";
import type { Heading, Root } from "mdast";
import { toString as mdastToString } from "mdast-util-to-string";
import remarkFrontmatter from "remark-frontmatter";
import remarkGfm from "remark-gfm";
import remarkParse from "remark-parse";
import { unified } from "unified";
import { visit } from "unist-util-visit";
import { HEADING_ID_PREFIX } from "@/lib/markdown-pipeline";

export interface TocEntry {
  depth: number;
  text: string;
  id: string;
}

/**
 * Text of a heading as the *rendered* side sees it.
 *
 * The ids have to agree with `rehype-slug`, which slugs
 * `hast-util-to-string(heading)` — the concatenation of the text nodes below
 * the heading element, and nothing else. Two of `mdast-util-to-string`'s
 * defaults would disagree with that:
 *
 * - `includeImageAlt`: an `![logo](logo.png)` in a heading becomes `<img
 *   alt="logo">`, an element with no text children, so the body's slug does
 *   not contain "logo";
 * - `includeHtml`: inline raw HTML (`## Setup <span>v2</span>`) is an `html`
 *   node holding the tag as *text* in mdast, while `rehype-raw` turns it into
 *   a real element in the body — where only the "v2" between the tags counts.
 *
 * The result is deliberately **not** trimmed: `hast-util-to-string` does not
 * trim either, and github-slugger turns a leading space into a leading `-`
 * (`## ![logo](l.png) Setup` → `-setup` on both sides). Trimming happens only
 * for the label this TOC displays.
 */
function headingText(node: Heading): string {
  return mdastToString(node, { includeImageAlt: false, includeHtml: false });
}

/**
 * Extract table-of-contents entries from a README's headings (`#` through `######`).
 *
 * `id` is assigned by `github-slugger` with `HEADING_ID_PREFIX` (`user-content-`)
 * prepended. This uses the same algorithm and the same prefix as rehype-slug (the
 * README body's rendering side, lib/markdown-pipeline.ts), so the id produced here
 * matches the id on the corresponding heading element in the rendered body exactly,
 * and a `#id` link scrolls to it correctly. A fresh `GithubSlugger` instance must be
 * created per document (the numbering it appends to duplicate headings depends on
 * the instance's internal state, so reusing one across documents throws the
 * numbering off).
 *
 * For the same reason **every heading is fed to the slugger, including the ones
 * that never reach the returned list**: those outside `minDepth`/`maxDepth` and
 * those with no text. rehype-slug slugs all of `h1`–`h6` and gives even an empty
 * heading an id (`user-content-`), so skipping one here before slugging it would
 * shift the `-1`, `-2`, … suffixes of every later duplicate and send TOC links to
 * the wrong heading. Filtering therefore happens *after* `slugger.slug()`.
 *
 * A `#` inside a code fence or content inside YAML frontmatter never appears in
 * remark's parse output, so neither is ever misdetected as a heading.
 *
 * Known limitation — **block-level raw HTML headings**: `<h2 align="center">Setup</h2>`
 * is a single `html` node to remark, so it is invisible here, while `rehype-raw`
 * makes it a real heading in the body that rehype-slug slugs. Such a document
 * gets no TOC entry for that heading, and if its text repeats in a Markdown
 * heading the suffixes drift apart again. Fixing that properly means taking the
 * TOC from the same hast the body is rendered from (after `rehype-raw` +
 * `rehype-sanitize` + `rehype-slug`) rather than from mdast; react-markdown does
 * not expose that tree, so it would mean running the whole pipeline a second time
 * per render.
 */
export function extractToc(
  source: string,
  { minDepth = 1, maxDepth = 3 }: { minDepth?: number; maxDepth?: number } = {},
): TocEntry[] {
  const tree = unified()
    .use(remarkParse)
    .use(remarkFrontmatter)
    .use(remarkGfm)
    .parse(source) as Root;

  const slugger = new GithubSlugger();
  const entries: TocEntry[] = [];

  visit(tree, "heading", (node: Heading) => {
    // Slug first, filter second — the slugger is stateful (see above).
    // rehype-slug runs with the same prefix, so this id is exactly what the
    // rendered heading carries.
    const text = headingText(node);
    const id = HEADING_ID_PREFIX + slugger.slug(text);
    if (node.depth < minDepth || node.depth > maxDepth) return;
    const label = text.trim();
    if (!label) return;
    entries.push({ depth: node.depth, text: label, id });
  });

  return entries;
}
