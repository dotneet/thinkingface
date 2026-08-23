import type { Nodes as HastNodes } from "hast";
import type { Root as MdastRoot } from "mdast";
import remarkFrontmatter from "remark-frontmatter";
import remarkParse from "remark-parse";
import remarkRehype from "remark-rehype";
import { unified } from "unified";
import { describe, expect, it } from "vitest";
import {
  HEADING_ID_PREFIX,
  hastText,
  markdownRehypePlugins,
  markdownRemarkPlugins,
  remarkStripFrontmatter,
} from "@/lib/markdown-pipeline";

const CARD = `---
license: mit
tags:
  - text-classification
---

# Model card

body text
`;

function text(node: HastNodes): string {
  return hastText(node);
}

function render(markdown: string, stripFrontmatter: boolean): string {
  const processor = unified()
    .use(remarkParse)
    .use(markdownRemarkPlugins(stripFrontmatter))
    .use(remarkRehype, { allowDangerousHtml: true });
  return text(processor.runSync(processor.parse(markdown)));
}

describe("remarkStripFrontmatter", () => {
  it("removes the yaml node remark-frontmatter parsed out", () => {
    const processor = unified()
      .use(remarkParse)
      .use(remarkFrontmatter, ["yaml"])
      .use(remarkStripFrontmatter);
    const tree = processor.runSync(processor.parse(CARD)) as MdastRoot;
    expect(tree.children.some((c: MdastRoot["children"][number]) => c.type === "yaml")).toBe(false);
    expect(tree.children[0]?.type).toBe("heading");
  });
});

describe("markdownRemarkPlugins", () => {
  it("keeps front matter out of the rendered body when stripping", () => {
    const out = render(CARD, true);
    expect(out).not.toContain("license: mit");
    expect(out).not.toContain("text-classification");
    expect(out).toContain("Model card");
    expect(out).toContain("body text");
  });

  it("without stripping, `---` is a thematic break and the body is misread", () => {
    // Documents the bug the `stripFrontmatter` flag exists to avoid: to
    // CommonMark this is a horizontal rule plus a setext heading, so the
    // metadata ends up as a giant title.
    const out = render(CARD, false);
    expect(out).toContain("license: mit");
  });

  it("leaves a mid-document `---` alone", () => {
    const out = render("intro\n\n---\n\noutro\n", true);
    expect(out).toContain("intro");
    expect(out).toContain("outro");
  });

  it("still parses GFM tables and task lists", () => {
    const out = render("| a |\n|---|\n| 1 |\n\n- [x] done\n", true);
    expect(out).toContain("a");
    expect(out).toContain("done");
  });
});

describe("hastText", () => {
  it("flattens an element subtree to its text", () => {
    expect(
      hastText({
        type: "element",
        tagName: "h2",
        properties: {},
        children: [
          { type: "text", value: "Getting " },
          {
            type: "element",
            tagName: "code",
            properties: {},
            children: [{ type: "text", value: "started" }],
          },
        ],
      }),
    ).toBe("Getting started");
  });
});

describe("markdownRehypePlugins", () => {
  function elements(
    markdown: string,
  ): Array<{ tagName: string; properties: Record<string, unknown> }> {
    const processor = unified()
      .use(remarkParse)
      .use(markdownRemarkPlugins(false))
      .use(remarkRehype, { allowDangerousHtml: true })
      .use(markdownRehypePlugins());
    const tree = processor.runSync(processor.parse(markdown));
    const out: Array<{ tagName: string; properties: Record<string, unknown> }> = [];
    const walk = (node: HastNodes) => {
      if (node.type === "element") out.push({ tagName: node.tagName, properties: node.properties });
      if ("children" in node) for (const child of node.children) walk(child);
    };
    walk(tree);
    return out;
  }

  it("namespaces heading ids so author text cannot clobber DOM globals", () => {
    const els = elements("# location\n\n## constructor\n");
    const h1 = els.find((e) => e.tagName === "h1");
    const h2 = els.find((e) => e.tagName === "h2");
    expect(h1?.properties.id).toBe(`${HEADING_ID_PREFIX}location`);
    expect(h2?.properties.id).toBe(`${HEADING_ID_PREFIX}constructor`);
    // …and the permalink anchor points at the namespaced id.
    const anchor = els.find(
      (e) =>
        e.tagName === "a" &&
        Array.isArray(e.properties.className) &&
        e.properties.className.includes("tf-heading-anchor"),
    );
    expect(anchor?.properties.href).toBe(`#${HEADING_ID_PREFIX}location`);
    expect(anchor?.properties["data-heading"]).toBe("location");
  });

  it("does not leave an author-supplied id on raw HTML", () => {
    const els = elements('<div id="location">x</div>\n');
    const div = els.find((e) => e.tagName === "div");
    expect(div).toBeDefined();
    expect(div?.properties.id).toBeUndefined();
  });
});
