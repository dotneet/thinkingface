import { defaultSchema, type Options as SanitizeSchema } from "rehype-sanitize";

/**
 * Sanitisation schema for repository Markdown.
 *
 * Model / dataset cards on the Hub are written with raw HTML in them —
 * `<div align="center">` banners, `<details><summary>` collapsibles, `<img>`
 * badges, `<video>` demos, `<kbd>`, `<sup>`/`<sub>`. `rehype-raw` turns that
 * HTML into real nodes, which means an untrusted repository could otherwise
 * inject script into the page: sanitisation is not optional once raw HTML is
 * enabled.
 *
 * The starting point is `hast-util-sanitize`'s `defaultSchema` (GitHub's
 * allowlist), widened only where a real card needs it:
 *
 * - `video` / `figure` / `figcaption` / `caption` / `u` / `mark` / `small` /
 *   `abbr` tags, none of which are in the default list;
 * - the media attributes those need (`src`, `controls`, `loop`, `muted`,
 *   `autoPlay`, `poster`, …). `align`, `width`, `height`, `alt`, `title` and
 *   `open` are already global in the default schema;
 * - the `math-inline` / `math-display` class names `remark-math` puts on
 *   `code` / `div` / `span`, without which `rehype-katex` (which runs *after*
 *   sanitisation) would find nothing left to typeset.
 *
 * and narrowed where the default is more permissive than we want:
 *
 * - `href` protocols drop `irc` / `ircs` / `xmpp`, leaving http / https /
 *   mailto (plus relative URLs, which carry no protocol and are always kept).
 *
 * What stays forbidden: `script` (stripped outright), `iframe` / `object` /
 * `embed` / `form` / `style` (never in the allowlist), the `style` attribute,
 * and every `on*` event handler — the schema is an allowlist, so anything not
 * named here is dropped.
 *
 * Ordering note: this must run **before** `rehype-slug`,
 * `rehype-autolink-headings`, `rehype-highlight` and `rehype-katex`. Classes
 * and ids added by those plugins are ours, not the author's, so they need no
 * sanitising — and running sanitisation last would strip them.
 */
export const markdownSanitizeSchema: SanitizeSchema = {
  ...defaultSchema,
  // The default rewrites every `id` to `user-content-…` as DOM-clobbering
  // defence, but it only rewrites the *targets*, never the `href="#…"`
  // pointing at them. GFM footnotes arrive from `mdast-util-to-hast` already
  // namespaced (`#user-content-fn-1`), so prefixing again produces
  // `id="user-content-user-content-fn-1"` and every footnote link lands
  // nowhere. So `id` is taken out of the clobber list — and, to keep the
  // clobbering defence, out of the author's hands entirely: the attribute
  // allowlist below only admits the ids the footnote plugin itself writes.
  // Heading ids are added by `rehype-slug` *after* sanitising.
  clobber: ["name"],
  tagNames: [
    ...(defaultSchema.tagNames ?? []),
    "abbr",
    "caption",
    "figcaption",
    "figure",
    "mark",
    "small",
    "u",
    "video",
  ],
  attributes: {
    ...defaultSchema.attributes,
    // No author-supplied `id` anywhere (see `clobber` above) …
    "*": (defaultSchema.attributes?.["*"] ?? []).filter((attr) => attr !== "id"),
    // … except the ones GFM footnotes are built from, recognisable by the
    // `user-content-` namespace `mdast-util-to-hast` gives them.
    li: [...(defaultSchema.attributes?.li ?? []), ["id", /^user-content-fn-/]],
    a: [...(defaultSchema.attributes?.a ?? []), ["id", /^user-content-fnref-/]],
    h2: [["id", "footnote-label"]],
    // `remark-math` marks inline math as `<code class="language-math
    // math-inline">`; the default schema only allows `language-*` on `code`.
    code: [["className", /^language-./, "math-inline", "math-display"]],
    div: [...(defaultSchema.attributes?.div ?? []), ["className", "math-inline", "math-display"]],
    span: [["className", "math-inline", "math-display"]],
    img: [...(defaultSchema.attributes?.img ?? []), "src", "loading", "decoding"],
    source: ["media", "sizes", "src", "srcSet", "type"],
    video: ["autoPlay", "controls", "loop", "muted", "playsInline", "poster", "preload", "src"],
  },
  protocols: {
    ...defaultSchema.protocols,
    href: ["http", "https", "mailto"],
    poster: ["http", "https"],
    src: ["http", "https"],
  },
};
