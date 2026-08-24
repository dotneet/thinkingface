import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import ReactMarkdown from "react-markdown";
import { describe, expect, it } from "vitest";
import { markdownRehypePlugins, markdownRemarkPlugins } from "@/lib/markdown-pipeline";
import { extractToc } from "@/lib/markdown-toc";

/**
 * The heading ids the README body really renders with, produced by running the
 * actual pipeline — the same plugin lists `components/ui/markdown.tsx` hands to
 * react-markdown, with `stripFrontmatter` on as `ReadmeCard` uses it.
 *
 * A TOC entry is only useful if its id is one of these, so the tests below
 * compare against this instead of against hand-written strings: if a pipeline
 * change ever moves the body's ids, the comparison fails rather than quietly
 * going out of step.
 */
function bodyHeadingIds(source: string): string[] {
  const html = renderToStaticMarkup(
    createElement(
      ReactMarkdown,
      { remarkPlugins: markdownRemarkPlugins(true), rehypePlugins: markdownRehypePlugins() },
      source,
    ),
  );
  return [...html.matchAll(/<h[1-6][^>]*\bid="([^"]*)"/g)].map((match) => match[1] ?? "");
}

describe("extractToc", () => {
  it("extracts headings within the default depth range", () => {
    const source = ["# Title", "", "## Usage", "", "### Details", "", "#### Too deep"].join("\n");
    expect(extractToc(source)).toEqual([
      { depth: 1, text: "Title", id: "user-content-title" },
      { depth: 2, text: "Usage", id: "user-content-usage" },
      { depth: 3, text: "Details", id: "user-content-details" },
    ]);
  });

  it("gives duplicate headings the same suffix as rehype-slug (-1, -2, ...)", () => {
    const source = ["# Usage", "", "# Usage", "", "# Usage"].join("\n");
    expect(extractToc(source).map((e) => e.id)).toEqual([
      "user-content-usage",
      "user-content-usage-1",
      "user-content-usage-2",
    ]);
  });

  it("ignores # inside a fenced code block", () => {
    const source = ["# Real Heading", "", "```", "# not a heading", "```", "", "## Also Real"].join(
      "\n",
    );
    expect(extractToc(source).map((e) => e.text)).toEqual(["Real Heading", "Also Real"]);
  });

  it("ignores YAML front matter", () => {
    const source = [
      "---",
      "title: My Model",
      "tags:",
      "  - # not a heading",
      "---",
      "",
      "# Real",
    ].join("\n");
    expect(extractToc(source)).toEqual([{ depth: 1, text: "Real", id: "user-content-real" }]);
  });

  it("respects a narrower maxDepth", () => {
    const source = ["# One", "## Two", "### Three"].join("\n");
    expect(extractToc(source, { maxDepth: 2 }).map((e) => e.depth)).toEqual([1, 2]);
  });

  it("respects minDepth", () => {
    const source = ["# One", "## Two", "### Three"].join("\n");
    expect(extractToc(source, { minDepth: 2, maxDepth: 3 }).map((e) => e.depth)).toEqual([2, 3]);
  });

  it("strips inline formatting from heading text but keeps the plain text", () => {
    const source = "## `code` and **bold**";
    expect(extractToc(source)).toEqual([
      { depth: 2, text: "code and bold", id: "user-content-code-and-bold" },
    ]);
  });

  it("returns an empty list for a README with no headings", () => {
    expect(extractToc("just a paragraph, no headings here.")).toEqual([]);
  });

  it("keeps the numbering in step with the body when a duplicate is deeper than maxDepth", () => {
    const source = ["## Setup", "", "#### Setup", "", "## Setup"].join("\n");
    const ids = bodyHeadingIds(source);
    expect(ids).toHaveLength(3);
    // The `h4` is outside the default depth range, so it gets no TOC entry —
    // but it still takes a slug in the body, which makes the second visible
    // "Setup" the body's *third* id, not its second.
    expect(extractToc(source).map((e) => e.id)).toEqual([ids[0], ids[2]]);
    expect(extractToc(source).map((e) => e.depth)).toEqual([2, 2]);
  });

  it("keeps the numbering in step with the body when a heading has no text", () => {
    const source = ["##", "", "## 🎉", "", "## Setup"].join("\n");
    const ids = bodyHeadingIds(source);
    // rehype-slug does not skip an empty heading: it gets `user-content-` and
    // advances the slugger, so the TOC has to advance it too — an emoji-only
    // heading slugs to the empty string as well and collides with it.
    expect(ids).toEqual(["user-content-", "user-content--1", "user-content-setup"]);
    const toc = extractToc(source);
    expect(toc.map((e) => e.text)).toEqual(["🎉", "Setup"]);
    expect(toc.map((e) => e.id)).toEqual([ids[1], ids[2]]);
  });

  it("matches the body's id when a heading holds an image or inline HTML", () => {
    const source = ["## ![logo](logo.png) Setup", "", "## Ready <span>now</span>"].join("\n");
    // The body slugs the text nodes only: an image contributes nothing (not
    // even its alt), and inline HTML contributes what is between the tags.
    expect(extractToc(source).map((e) => e.id)).toEqual(bodyHeadingIds(source));
    expect(extractToc(source).map((e) => e.text)).toEqual(["Setup", "Ready now"]);
  });

  it("known limitation: a block-level raw HTML heading is invisible to the TOC", () => {
    const source = ['<h2 align="center">Setup</h2>', "", "## Setup"].join("\n");
    // `rehype-raw` makes the HTML heading a real heading in the body, so it
    // takes the plain `user-content-setup` slug and pushes the Markdown
    // heading to `-1`. remark sees only the Markdown heading, so the TOC has
    // one entry and it links to the HTML heading above it. This is documented
    // on `extractToc`; fixing it means extracting the TOC from the rendered
    // hast instead of from mdast, and this test should then be updated to
    // expect two matching entries rather than treated as a regression.
    expect(bodyHeadingIds(source)).toEqual(["user-content-setup", "user-content-setup-1"]);
    expect(extractToc(source).map((e) => e.id)).toEqual(["user-content-setup"]);
  });
});
