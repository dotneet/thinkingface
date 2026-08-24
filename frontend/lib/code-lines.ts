/**
 * Line splitting for the source-file preview, with no highlighter attached.
 *
 * Deliberately separate from `lib/code-highlight.ts`. That module imports
 * `rehype-highlight`, which pulls in lowlight's whole `common` grammar set --
 * fine on the server, but any client component that imports *anything* from it
 * drags all of that into the browser bundle, because the import sits at module
 * scope and the grammars are not tree-shakeable. `TextPreview` is a client
 * component and needs only the pieces here, so they live where importing them
 * costs nothing.
 *
 * Framework-free, like the module it was split out of.
 */
import type { ElementContent } from "hast";

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
 * The numbered lines of a file with no highlighting applied -- the shape
 * `buildCodeLines` returns for a file whose language is unknown, built without
 * loading a highlighter. Used by client callers whose text only arrives in the
 * browser (the tabular preview's Raw mode over a fetched full file).
 */
export function plainCodeLines(code: string): CodeHighlightResult {
  const total = countLines(code);
  if (code.length > MAX_HIGHLIGHT_CHARS) return { ok: false, reason: "tooLarge", lines: total };
  if (total > MAX_HIGHLIGHT_LINES) return { ok: false, reason: "tooManyLines", lines: total };
  return {
    ok: true,
    language: null,
    lines: splitHastLines([{ type: "text", value: code }]).map((line, index) => ({
      number: index + 1,
      nodes: line,
    })),
  };
}
