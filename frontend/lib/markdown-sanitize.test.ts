import type { Nodes } from "hast";
import rehypeRaw from "rehype-raw";
import rehypeSanitize from "rehype-sanitize";
import remarkParse from "remark-parse";
import remarkRehype from "remark-rehype";
import { unified } from "unified";
import { describe, expect, it } from "vitest";
import { markdownSanitizeSchema } from "@/lib/markdown-sanitize";

/**
 * Minimal HTML serialiser. `hast-util-to-html` is not a dependency of this
 * app, and the assertions here only need tag names and attributes.
 */
function toHtml(node: Nodes): string {
  if (node.type === "text") return node.value;
  if (node.type === "element") {
    const attrs = Object.entries(node.properties ?? {})
      .filter(([, v]) => v !== null && v !== undefined && v !== false)
      .map(([k, v]) => ` ${k}="${Array.isArray(v) ? v.join(" ") : String(v)}"`)
      .join("");
    return `<${node.tagName}${attrs}>${node.children.map(toHtml).join("")}</${node.tagName}>`;
  }
  if ("children" in node) return node.children.map(toHtml).join("");
  return "";
}

function render(markdown: string): string {
  const processor = unified()
    .use(remarkParse)
    .use(remarkRehype, { allowDangerousHtml: true })
    .use(rehypeRaw)
    .use(rehypeSanitize, markdownSanitizeSchema);
  return toHtml(processor.runSync(processor.parse(markdown)));
}

describe("markdownSanitizeSchema", () => {
  describe("drops anything executable", () => {
    it("removes script elements and their contents", () => {
      const html = render("before\n\n<script>alert(1)</script>\n\nafter");
      expect(html).not.toContain("script");
      expect(html).not.toContain("alert(1)");
    });

    it("removes inline event handlers", () => {
      const html = render('<img src="https://x.test/a.png" onerror="alert(1)">');
      expect(html).toContain("https://x.test/a.png");
      expect(html).not.toContain("onerror");
      expect(html).not.toContain("alert");
    });

    it("removes javascript: hrefs", () => {
      const html = render('<a href="javascript:alert(1)">click</a>');
      expect(html).toContain("click");
      expect(html).not.toContain("javascript:");
    });

    it("removes the style attribute", () => {
      const html = render('<div style="position:fixed;inset:0">x</div>');
      expect(html).toContain("<div>");
      expect(html).not.toContain("position:fixed");
    });

    it("removes iframe, object and embed", () => {
      const html = render(
        '<iframe src="https://x.test"></iframe><object data="x"></object><embed src="x">',
      );
      expect(html).not.toContain("iframe");
      expect(html).not.toContain("object");
      expect(html).not.toContain("embed");
    });

    it("narrows link protocols to http, https and mailto", () => {
      expect(render('<a href="https://x.test/">a</a>')).toContain("https://x.test/");
      expect(render('<a href="mailto:a@x.test">a</a>')).toContain("mailto:a@x.test");
      // `irc` and `xmpp` are in hast-util-sanitize's default allowlist.
      expect(render('<a href="irc://x.test/room">a</a>')).not.toContain("irc://");
      expect(render('<a href="data:text/html,<b>x</b>">a</a>')).not.toContain("data:");
    });

    it("keeps relative URLs, which carry no protocol", () => {
      expect(render('<a href="docs/usage.md">a</a>')).toContain('href="docs/usage.md"');
    });
  });

  describe("keeps what Hub cards are actually written with", () => {
    it("keeps a centred banner div with its image", () => {
      const html = render(
        '<div align="center"><img src="https://x.test/logo.png" width="200"></div>',
      );
      expect(html).toContain('<div align="center">');
      expect(html).toContain('src="https://x.test/logo.png"');
      expect(html).toContain('width="200"');
    });

    it("keeps details / summary collapsibles", () => {
      const html = render("<details open><summary>Citation</summary>\n\nbody\n\n</details>");
      expect(html).toContain("<details");
      expect(html).toContain("<summary>");
      expect(html).toContain("open");
    });

    it("keeps p[align], br, sup, sub and kbd", () => {
      const html = render('<p align="center">a<br>b<sup>1</sup><sub>2</sub><kbd>Ctrl</kbd></p>');
      expect(html).toContain('align="center"');
      expect(html).toContain("<br>");
      expect(html).toContain("<sup>");
      expect(html).toContain("<sub>");
      expect(html).toContain("<kbd>");
    });

    it("keeps video with its playback attributes", () => {
      const html = render(
        '<video src="https://x.test/demo.mp4" controls loop muted poster="https://x.test/p.png"></video>',
      );
      expect(html).toContain("<video");
      expect(html).toContain("https://x.test/demo.mp4");
      expect(html).toContain("controls");
      expect(html).toContain("loop");
      expect(html).toContain("https://x.test/p.png");
    });

    it("drops srcset, the one URL attribute nothing resolves or protocol-checks", () => {
      // react-markdown's urlTransform only rewrites html-url-attributes
      // (src / href / poster / …), and the protocol allowlist checks a single
      // URL, not a candidate list — so a surviving `srcset` would reach the
      // DOM unresolved and unexamined. `<picture>` falls back to the nested
      // `<img>`, which gets both.
      const html = render(
        '<source media="(min-width: 800px)" srcset="./wide.png 2x" src="./wide.png" type="image/png">',
      );
      expect(html).not.toContain("srcset");
      expect(html).not.toContain("srcSet");
      expect(html).toContain("media");
      expect(html).toContain("./wide.png");
    });

    it("keeps the math classes rehype-katex looks for", () => {
      // remark-math emits these; rehype-katex runs after sanitising, so if the
      // classes were stripped here no formula would ever be typeset.
      const html = render('<code class="language-math math-inline">a^2</code>');
      expect(html).toContain("language-math");
      expect(html).toContain("math-inline");
    });

    it("keeps a disabled task-list checkbox", () => {
      const html = render(
        '<ul class="contains-task-list"><li class="task-list-item">' +
          '<input type="checkbox" disabled> todo</li></ul>',
      );
      expect(html).toContain("checkbox");
      expect(html).toContain("disabled");
      expect(html).toContain("task-list-item");
      expect(html).toContain("contains-task-list");
    });

    it("keeps GFM footnote ids as written, so footnote links still point at their notes", () => {
      // hast-util-sanitize's default `clobber` would rewrite the note's id to
      // `user-content-user-content-fn-1` while leaving the `href` untouched.
      const html = render(
        '<sup><a href="#user-content-fn-1" id="user-content-fnref-1">1</a></sup>' +
          '<section class="footnotes"><h2 id="footnote-label">Footnotes</h2>' +
          '<li id="user-content-fn-1">note</li></section>',
      );
      expect(html).toContain('id="user-content-fn-1"');
      expect(html).toContain('id="user-content-fnref-1"');
      expect(html).toContain('id="footnote-label"');
      expect(html).not.toContain("user-content-user-content");
    });

    it("keeps sr-only on the footnote heading, which is meant to be invisible", () => {
      // The default schema already allows `class="sr-only"` on h2; adding the
      // footnote id used to *replace* that entry, so GFM's visually hidden
      // "Footnotes" heading showed up as an ordinary heading in the card.
      const html = render(
        '<section class="footnotes"><h2 class="sr-only" id="footnote-label">Footnotes</h2>' +
          '<li id="user-content-fn-1">note</li></section>',
      );
      expect(html).toContain('className="sr-only"');
      expect(html).toContain('id="footnote-label"');
    });

    it("drops author-supplied ids (DOM clobbering), on any element", () => {
      // `<div id="location">` would define `window.location`-style globals if
      // it survived; only the footnote plugin's namespaced ids are admitted.
      const html = render(
        '<div id="location">x</div><a id="evil" href="https://x.test/">a</a>' +
          '<li id="fn-1">y</li><h2 id="custom">h</h2>',
      );
      expect(html).not.toContain('id="location"');
      expect(html).not.toContain('id="evil"');
      expect(html).not.toContain('id="fn-1"');
      expect(html).not.toContain('id="custom"');
      expect(html).toContain("<div>x</div>");
    });

    it("namespaces author-supplied anchor names instead of letting them clobber", () => {
      const html = render('<a name="top">a</a>');
      expect(html).toContain('name="user-content-top"');
    });

    it("keeps language-* on fenced code so highlighting has something to key off", () => {
      const html = render("```python\nprint(1)\n```");
      expect(html).toContain("language-python");
    });
  });
});
