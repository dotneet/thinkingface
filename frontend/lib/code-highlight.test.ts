import type { Element, ElementContent } from "hast";
import { describe, expect, it } from "vitest";
import {
  buildCodeLines,
  countLines,
  detectCodeLanguage,
  MAX_HIGHLIGHT_CHARS,
  MAX_HIGHLIGHT_LINES,
  splitHastLines,
} from "@/lib/code-highlight";

/** Flattens a line's hast nodes back to the text they render. */
function lineText(nodes: ElementContent[]): string {
  return nodes
    .map((node) => {
      if (node.type === "text") return node.value;
      if (node.type === "element") return lineText(node.children);
      return "";
    })
    .join("");
}

function element(tagName: string, children: ElementContent[]): Element {
  return { type: "element", tagName, properties: { className: ["hljs-string"] }, children };
}

describe("detectCodeLanguage", () => {
  it("maps the common source extensions", () => {
    expect(detectCodeLanguage("train.py")).toBe("python");
    expect(detectCodeLanguage("main.go")).toBe("go");
    expect(detectCodeLanguage("config.json")).toBe("json");
    expect(detectCodeLanguage("lib/api.ts")).toBe("typescript");
    expect(detectCodeLanguage("component.tsx")).toBe("typescript");
    expect(detectCodeLanguage("model.yaml")).toBe("yaml");
    expect(detectCodeLanguage("pyproject.toml")).toBe("ini");
    expect(detectCodeLanguage("index.html")).toBe("xml");
  });

  it("is case-insensitive and ignores directories", () => {
    expect(detectCodeLanguage("SRC/Train.PY")).toBe("python");
    expect(detectCodeLanguage("deep/nested/dir/query.SQL")).toBe("sql");
  });

  it("recognises well-known extensionless names", () => {
    expect(detectCodeLanguage("Makefile")).toBe("makefile");
    expect(detectCodeLanguage("GNUmakefile")).toBe("makefile");
    expect(detectCodeLanguage("Rakefile")).toBe("ruby");
    expect(detectCodeLanguage(".zshrc")).toBe("bash");
  });

  it("returns null for a language rehype-highlight cannot colour", () => {
    // `dockerfile` is a real highlight.js grammar but is not in lowlight's
    // `common` set, and lowlight may not be imported directly. Plain text with
    // a gutter beats a language label that colours nothing.
    expect(detectCodeLanguage("Dockerfile")).toBeNull();
    expect(detectCodeLanguage("CMakeLists.txt")).toBeNull();
  });

  it("returns null when there is nothing to go on", () => {
    expect(detectCodeLanguage("LICENSE")).toBeNull();
    expect(detectCodeLanguage("notes.txt")).toBeNull();
    expect(detectCodeLanguage("model.safetensors")).toBeNull();
    expect(detectCodeLanguage("")).toBeNull();
  });
});

describe("countLines", () => {
  it("treats a trailing newline as a terminator, not a new line", () => {
    expect(countLines("a\nb\n")).toBe(2);
    expect(countLines("a\nb")).toBe(2);
    expect(countLines("a\n\n")).toBe(2);
    expect(countLines("single")).toBe(1);
    expect(countLines("")).toBe(0);
  });
});

describe("splitHastLines", () => {
  it("splits a plain text node on newlines", () => {
    const lines = splitHastLines([{ type: "text", value: "one\ntwo\nthree" }]);
    expect(lines.map(lineText)).toEqual(["one", "two", "three"]);
  });

  it("keeps empty lines as empty entries", () => {
    const lines = splitHastLines([{ type: "text", value: "one\n\nthree" }]);
    expect(lines).toHaveLength(3);
    expect(lines[1]).toEqual([]);
  });

  it("clones a token span that straddles a newline onto both lines", () => {
    const lines = splitHastLines([
      { type: "text", value: "x = " },
      element("span", [{ type: "text", value: '"""a\nb"""' }]),
      { type: "text", value: "\ny" },
    ]);
    expect(lines.map(lineText)).toEqual(['x = """a', 'b"""', "y"]);
    // Both halves keep the token classes, or the second line loses its colour.
    for (const index of [0, 1]) {
      const span = (lines[index] as ElementContent[]).find((n) => n.type === "element");
      expect(span?.type === "element" && span.properties?.className).toEqual(["hljs-string"]);
    }
  });

  it("drops only the phantom line a trailing newline would create", () => {
    expect(splitHastLines([{ type: "text", value: "a\nb\n" }]).map(lineText)).toEqual(["a", "b"]);
    expect(splitHastLines([{ type: "text", value: "a\nb\n\n" }]).map(lineText)).toEqual([
      "a",
      "b",
      "",
    ]);
  });
});

describe("buildCodeLines", () => {
  it("numbers lines from 1 and preserves the source text exactly", () => {
    const source = 'def main():\n    print("hi")\n';
    const result = buildCodeLines(source, "python");
    if (!result.ok) throw new Error(`expected ok, got ${result.reason}`);
    expect(result.lines.map((l) => l.number)).toEqual([1, 2]);
    expect(result.lines.map((l) => lineText(l.nodes)).join("\n")).toBe(source.trimEnd());
  });

  it("emits hljs token spans when a language is given", () => {
    const result = buildCodeLines('def main():\n    print("hi")\n', "python");
    if (!result.ok) throw new Error("expected ok");
    const classes = result.lines
      .flatMap((line) => line.nodes)
      .filter((node) => node.type === "element")
      .flatMap((node) => (node.type === "element" ? (node.properties?.className ?? []) : []));
    // highlight.js also emits bare modifier classes ("function_", "title"),
    // so this only asserts the prefixed ones the stylesheet targets are there.
    expect(classes.some((c) => String(c).startsWith("hljs-"))).toBe(true);
  });

  it("still numbers lines when the language is unknown", () => {
    const result = buildCodeLines("alpha\nbeta\n", null);
    if (!result.ok) throw new Error("expected ok");
    expect(result.language).toBeNull();
    expect(result.lines.map((l) => lineText(l.nodes))).toEqual(["alpha", "beta"]);
    // No language means no highlighting at all, not a guess.
    expect(result.lines.every((l) => l.nodes.every((n) => n.type === "text"))).toBe(true);
  });

  it("refuses to build the DOM for a file with too many lines", () => {
    const result = buildCodeLines("x\n".repeat(MAX_HIGHLIGHT_LINES + 1), "python");
    expect(result).toEqual({
      ok: false,
      reason: "tooManyLines",
      lines: MAX_HIGHLIGHT_LINES + 1,
    });
  });

  it("refuses to highlight one enormous line", () => {
    // 512KB of minified JSON is a single line, so the line cap alone is not a bound.
    const result = buildCodeLines("x".repeat(MAX_HIGHLIGHT_CHARS + 1), "json");
    if (result.ok) throw new Error("expected the character cap to reject this");
    expect(result.reason).toBe("tooLarge");
    expect(result.lines).toBe(1);
  });
});
