import { describe, expect, it } from "vitest";
import { extractToc } from "@/lib/markdown-toc";

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
});
