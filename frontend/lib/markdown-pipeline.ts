import type { Nodes as HastNodes } from "hast";
import type { Root as MdastRoot } from "mdast";
import rehypeAutolinkHeadings from "rehype-autolink-headings";
import rehypeHighlight from "rehype-highlight";
import rehypeKatex from "rehype-katex";
import rehypeRaw from "rehype-raw";
import rehypeSanitize from "rehype-sanitize";
import rehypeSlug from "rehype-slug";
import remarkFrontmatter from "remark-frontmatter";
import remarkGfm from "remark-gfm";
import remarkMath from "remark-math";
import type { PluggableList } from "unified";
import { markdownSanitizeSchema } from "@/lib/markdown-sanitize";

/**
 * Namespace for every id the pipeline writes into the document — heading ids
 * from `rehype-slug`, and the footnote ids `mdast-util-to-hast` already
 * produces. It is the same prefix GitHub uses, and for the same reason: ids
 * derived from author text must not be able to shadow `window.*` /
 * `document.*` properties (DOM clobbering). In-document `#fragment` links are
 * re-pointed at the prefixed ids by `<Markdown>`'s link mapping, and
 * `lib/markdown-toc.ts` builds its anchors with the same prefix.
 */
export const HEADING_ID_PREFIX = "user-content-";

/**
 * Plain text of a hast subtree. `hast-util-to-string` would do this, but it is
 * only present transitively; six lines here keep the dependency list honest.
 */
export function hastText(node: HastNodes): string {
  if (node.type === "text") return node.value;
  if ("children" in node) return node.children.map(hastText).join("");
  return "";
}

/**
 * Remove the YAML front matter block `remark-frontmatter` parsed out.
 *
 * `mdast-util-to-hast` already ignores `yaml` nodes, so this is belt and
 * braces — but the fallback for an unknown mdast node is "render its `value`
 * as text", and that is exactly how `license: mit` used to leak into the body.
 * Deleting the node makes the outcome independent of the mdast→hast handler
 * table.
 */
export function remarkStripFrontmatter() {
  return (tree: MdastRoot) => {
    tree.children = tree.children.filter((child) => child.type !== "yaml");
  };
}

/**
 * Remark half of the Markdown pipeline.
 *
 * `remark-frontmatter` is only enabled when the caller wants the block gone.
 * Without it a leading `---\nlicense: mit\n---` is not front matter at all to
 * CommonMark — it is a thematic break followed by a setext heading, which is
 * what the file preview used to render.
 */
export function markdownRemarkPlugins(stripFrontmatter = false): PluggableList {
  const plugins: PluggableList = [remarkGfm];
  if (stripFrontmatter) {
    plugins.push([remarkFrontmatter, ["yaml"]], remarkStripFrontmatter);
  }
  plugins.push(remarkMath);
  return plugins;
}

/**
 * Rehype half of the Markdown pipeline. The order is load-bearing:
 *
 * 1. `rehype-raw` re-parses the raw HTML that `remark-rehype` passed through,
 *    so `<div align="center">` and `<details>` become real nodes;
 * 2. `rehype-sanitize` immediately drops anything dangerous in what step 1
 *    just produced — nothing may run between these two;
 * 3. `rehype-slug` then adds heading ids, namespaced with
 *    {@link HEADING_ID_PREFIX}: a heading's text is author-controlled, so an
 *    unprefixed `# location` / `# constructor` would define a DOM-clobbering
 *    global. It runs *after* sanitising so the sanitiser's own clobber pass
 *    cannot prefix them a second time;
 * 4. `rehype-autolink-headings` appends the permalink anchor (rendered by
 *    `MarkdownHeadingAnchor`, which supplies the translated `aria-label`);
 * 5. `rehype-highlight` adds `hljs-*` spans — added after sanitising so the
 *    class names survive;
 * 6. `rehype-katex` replaces the `math-inline` / `math-display` nodes last.
 */
export function markdownRehypePlugins(): PluggableList {
  return [
    rehypeRaw,
    [rehypeSanitize, markdownSanitizeSchema],
    [rehypeSlug, { prefix: HEADING_ID_PREFIX }],
    [
      rehypeAutolinkHeadings,
      {
        behavior: "append",
        // The anchor's contents come from MarkdownHeadingAnchor instead of the
        // plugin's default `<span class="icon icon-link">`.
        content: [],
        properties: (element: HastNodes) => ({
          className: ["tf-heading-anchor"],
          "data-heading": hastText(element),
        }),
      },
    ],
    // `detect: false`: never guess a language for an unlabelled block — a
    // guess is wrong often enough to be noise. `math` is left alone so
    // rehype-katex still sees `language-math math-display` blocks.
    [rehypeHighlight, { detect: false, plainText: ["math", "text", "txt"] }],
    rehypeKatex,
  ];
}
