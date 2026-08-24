/**
 * Syntax highlighting and line splitting for the source-file preview.
 *
 * `rehype-highlight` is already a direct dependency (it colours the fenced
 * blocks inside a rendered README, see `lib/markdown-pipeline.ts`), so an
 * actual `.py` / `.go` / `.json` file gets the same treatment here instead of
 * being the only code in the app that renders as flat text. Nothing new is
 * installed for it: a minimal `root > pre > code.language-xxx` hast tree is run
 * through the same plugin, and the `hljs-*` spans it produces are handed back
 * to the caller to render.
 *
 * Everything below is framework-free — no React, no `useT` — so it can be unit
 * tested under vitest's node environment, and so a Server Component may import
 * it without crossing the `"use client"` boundary (DESIGN.md §6's
 * `client-boundary` rule).
 */

import type { ElementContent, Root } from "hast";
import rehypeHighlight from "rehype-highlight";
import { unified } from "unified";

/**
 * The most lines that get the gutter + highlighting treatment.
 *
 * This hub serves GB-scale models and datasets. The server already clips a
 * preview at 512KB (`maxRawPreviewBytes` in backend/internal/api/resolve.go),
 * but 512KB of short lines is well over 20,000 of them, and every highlighted
 * line here becomes a row element, an anchor and a handful of token spans —
 * order 10 DOM nodes each. 5,000 lines is roughly 50,000 nodes, which is the
 * point where layout and hydration cost start to show; it is also comfortably
 * more than any hand-written source file that fits in 512KB. Past it the
 * preview degrades to a plain `<pre>` and says so.
 */
export const MAX_HIGHLIGHT_LINES = 5000;

/**
 * The most characters handed to the highlighter, independent of line count.
 *
 * The line cap alone does not bound the work: 512KB of minified JSON is *one*
 * line, and highlight.js is a backtracking regex engine run over the whole
 * string. Half the server's preview limit keeps the pathological single-line
 * case out while leaving every realistic source file inside — a 256KB source
 * file is already past {@link MAX_HIGHLIGHT_LINES} anyway.
 */
export const MAX_HIGHLIGHT_CHARS = 256 * 1024;

/** One rendered source line: its 1-based number and its (highlighted) content. */
export type CodeLine = {
  number: number;
  /** hast children — only `element` (an `hljs-*` span) and `text` nodes appear. */
  nodes: ElementContent[];
};

/** Why a file was not given the gutter + highlighting treatment. */
export type CodeHighlightSkipReason = "tooManyLines" | "tooLarge";

/**
 * A reason code rather than a sentence: this module is framework-free, so the
 * caller does the translating — see `repo.codePreview.*` in the dictionaries.
 * Same shape and same reasoning as `lib/tabular.ts`.
 */
export type CodeHighlightResult =
  | { ok: true; language: string | null; lines: CodeLine[] }
  | { ok: false; reason: CodeHighlightSkipReason; lines: number };

/**
 * The languages `rehype-highlight` can actually colour here.
 *
 * It registers lowlight's `common` set by default, and this list mirrors it
 * exactly. Anything outside it makes the plugin emit a "not registered"
 * message and leave the block untouched, so detection filters against this set
 * up front: an honest plain-text render beats a language label that colours
 * nothing. `lowlight` itself is only a transitive dependency and must not be
 * imported directly, which is why the set is spelled out rather than read off
 * the package.
 */
const AVAILABLE_LANGUAGES = new Set([
  "arduino",
  "bash",
  "c",
  "cpp",
  "csharp",
  "css",
  "diff",
  "go",
  "graphql",
  "ini",
  "java",
  "javascript",
  "json",
  "kotlin",
  "less",
  "lua",
  "makefile",
  "markdown",
  "objectivec",
  "perl",
  "php",
  "php-template",
  "plaintext",
  "python",
  "python-repl",
  "r",
  "ruby",
  "rust",
  "scss",
  "shell",
  "sql",
  "swift",
  "typescript",
  "vbnet",
  "wasm",
  "xml",
  "yaml",
]);

/**
 * Whole-file names that carry their language without an extension. Matched
 * case-insensitively against the base name.
 *
 * `Dockerfile` is listed even though highlight.js's `dockerfile` grammar is not
 * in the common set: the mapping is the honest answer to "what language is
 * this", and {@link detectCodeLanguage} filters it out afterwards, so the file
 * renders as plain text with a gutter rather than mis-coloured as shell.
 */
const LANGUAGE_BY_NAME: Record<string, string> = {
  dockerfile: "dockerfile",
  containerfile: "dockerfile",
  makefile: "makefile",
  gnumakefile: "makefile",
  rakefile: "ruby",
  gemfile: "ruby",
  brewfile: "ruby",
  vagrantfile: "ruby",
  cmakelists: "cmake",
  ".gitconfig": "ini",
  ".editorconfig": "ini",
  ".npmrc": "ini",
  ".bashrc": "bash",
  ".bash_profile": "bash",
  ".zshrc": "bash",
  ".profile": "bash",
};

/** Extension (without the dot, lowercased) → highlight.js language name. */
const LANGUAGE_BY_EXTENSION: Record<string, string> = {
  bash: "bash",
  sh: "bash",
  zsh: "bash",
  fish: "shell",
  c: "c",
  h: "c",
  cc: "cpp",
  cpp: "cpp",
  cxx: "cpp",
  hpp: "cpp",
  hh: "cpp",
  hxx: "cpp",
  cs: "csharp",
  css: "css",
  diff: "diff",
  patch: "diff",
  go: "go",
  graphql: "graphql",
  gql: "graphql",
  cfg: "ini",
  conf: "ini",
  ini: "ini",
  properties: "ini",
  toml: "ini",
  java: "java",
  cjs: "javascript",
  js: "javascript",
  jsx: "javascript",
  mjs: "javascript",
  ipynb: "json",
  json: "json",
  jsonc: "json",
  kt: "kotlin",
  kts: "kotlin",
  less: "less",
  lua: "lua",
  mk: "makefile",
  markdown: "markdown",
  md: "markdown",
  mdx: "markdown",
  m: "objectivec",
  mm: "objectivec",
  pl: "perl",
  pm: "perl",
  php: "php",
  py: "python",
  pyi: "python",
  pyw: "python",
  r: "r",
  gemspec: "ruby",
  rb: "ruby",
  rs: "rust",
  scss: "scss",
  sql: "sql",
  swift: "swift",
  cts: "typescript",
  mts: "typescript",
  ts: "typescript",
  tsx: "typescript",
  vb: "vbnet",
  wast: "wasm",
  wat: "wasm",
  htm: "xml",
  html: "xml",
  plist: "xml",
  svg: "xml",
  xml: "xml",
  xsl: "xml",
  yaml: "yaml",
  yml: "yaml",
};

/**
 * The highlight.js language for a file, or null when there is nothing
 * trustworthy to say. Never guesses from the contents — `detect: false` is set
 * for the same reason it is set in the Markdown pipeline: a wrong guess is
 * noise, and here it would be noise over a whole file rather than one fence.
 */
export function detectCodeLanguage(fileName: string): string | null {
  const base = fileName.split("/").pop()?.toLowerCase() ?? "";
  if (base === "") return null;

  const byName = LANGUAGE_BY_NAME[base] ?? LANGUAGE_BY_NAME[base.replace(/\.[^.]+$/, "")];
  if (byName) return AVAILABLE_LANGUAGES.has(byName) ? byName : null;

  const dot = base.lastIndexOf(".");
  // `dot <= 0` covers both "no extension" and a leading-dot dotfile, whose
  // whole name is the name (and was already looked up above).
  if (dot <= 0) return null;
  const byExtension = LANGUAGE_BY_EXTENSION[base.slice(dot + 1)];
  if (!byExtension) return null;
  return AVAILABLE_LANGUAGES.has(byExtension) ? byExtension : null;
}

/**
 * Built once: `rehypeHighlight` instantiates lowlight (37 grammars) when the
 * plugin is attached, and rebuilding that per preview is pure waste.
 */
const processor = unified().use(rehypeHighlight, { detect: false });

/** Runs the plugin over `code` and returns the highlighted `<code>` children. */
function highlightNodes(code: string, language: string): ElementContent[] {
  const tree: Root = {
    type: "root",
    children: [
      {
        type: "element",
        tagName: "pre",
        properties: {},
        children: [
          {
            type: "element",
            tagName: "code",
            properties: { className: [`language-${language}`] },
            children: [{ type: "text", value: code }],
          },
        ],
      },
    ],
  };

  try {
    const out = processor.runSync(tree) as unknown as Root;
    const pre = out.children[0];
    if (pre?.type !== "element") return [{ type: "text", value: code }];
    const codeElement = pre.children[0];
    if (codeElement?.type !== "element") return [{ type: "text", value: code }];
    return codeElement.children;
  } catch {
    // A grammar that throws must cost colour, not the preview.
    return [{ type: "text", value: code }];
  }
}

/**
 * Appends one hast node to `lines`, opening a new line at every `\n`.
 *
 * A token span can straddle a newline (a block comment, a triple-quoted
 * string), so an element that spans lines is cloned onto each of them with the
 * fragment of its children that belongs there. That is what makes a per-line
 * gutter possible at all: every line has to be its own element to carry an
 * `id`.
 */
function appendNode(lines: ElementContent[][], node: ElementContent): void {
  const current = () => lines[lines.length - 1] as ElementContent[];

  if (node.type === "text") {
    const parts = node.value.split("\n");
    for (const [index, part] of parts.entries()) {
      if (index > 0) lines.push([]);
      if (part !== "") current().push({ type: "text", value: part });
    }
    return;
  }
  if (node.type !== "element") return; // comments / doctypes never appear here

  const inner: ElementContent[][] = [[]];
  for (const child of node.children) appendNode(inner, child);
  for (const [index, fragment] of inner.entries()) {
    if (index > 0) lines.push([]);
    if (fragment.length > 0) current().push({ ...node, children: fragment });
  }
}

/** Splits highlighted hast children into one entry per source line. */
export function splitHastLines(nodes: ElementContent[]): ElementContent[][] {
  const lines: ElementContent[][] = [[]];
  for (const node of nodes) appendNode(lines, node);
  // A trailing newline terminates the last line rather than starting a new
  // empty one — otherwise every well-formed file shows a phantom final line.
  if (lines.length > 1 && (lines[lines.length - 1] as ElementContent[]).length === 0) lines.pop();
  return lines;
}

/** Number of lines in `code`, counted without building anything. */
export function countLines(code: string): number {
  if (code === "") return 0;
  let count = 1;
  for (let i = 0; i < code.length; i++) if (code[i] === "\n") count++;
  // Same trailing-newline rule as splitHastLines.
  return code.endsWith("\n") ? count - 1 : count;
}

/**
 * The numbered (and, when the language is known, highlighted) lines of a file,
 * or the reason the file was too big to treat that way.
 */
export function buildCodeLines(code: string, language: string | null): CodeHighlightResult {
  const total = countLines(code);
  if (code.length > MAX_HIGHLIGHT_CHARS) return { ok: false, reason: "tooLarge", lines: total };
  if (total > MAX_HIGHLIGHT_LINES) return { ok: false, reason: "tooManyLines", lines: total };

  const nodes: ElementContent[] = language
    ? highlightNodes(code, language)
    : [{ type: "text", value: code }];

  return {
    ok: true,
    language,
    lines: splitHastLines(nodes).map((line, index) => ({ number: index + 1, nodes: line })),
  };
}
