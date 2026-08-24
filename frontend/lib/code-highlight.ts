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

import {
  type CodeHighlightResult,
  countLines,
  MAX_HIGHLIGHT_CHARS,
  MAX_HIGHLIGHT_LINES,
  splitHastLines,
} from "@/lib/code-lines";

// Re-exported so call sites and tests keep one import path for "everything
// about rendering a source file", even though the halves now live in two
// modules for bundling reasons (see lib/code-lines.ts).
export {
  type CodeHighlightResult,
  type CodeHighlightSkipReason,
  type CodeLine,
  countLines,
  MAX_HIGHLIGHT_CHARS,
  MAX_HIGHLIGHT_LINES,
  plainCodeLines,
  splitHastLines,
} from "@/lib/code-lines";

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
