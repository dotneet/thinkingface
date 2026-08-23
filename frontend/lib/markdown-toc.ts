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
 * A `#` inside a code fence or content inside YAML frontmatter never appears in
 * remark's parse output, so neither is ever misdetected as a heading. Raw HTML like
 * `<h2>` is also excluded (remark treats it as an html node, not a heading node).
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
    if (node.depth < minDepth || node.depth > maxDepth) return;
    const text = mdastToString(node).trim();
    if (!text) return;
    // rehype-slug runs with the same prefix, so this id is exactly what the
    // rendered heading carries.
    entries.push({ depth: node.depth, text, id: HEADING_ID_PREFIX + slugger.slug(text) });
  });

  return entries;
}
