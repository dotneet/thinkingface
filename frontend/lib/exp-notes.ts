/**
 * `[text](run:<run name>)` links in a project's NOTES.md
 * (docs/dev/agent-features.md §2.7).
 *
 * The notebook is written by people and by agents that know run names, not
 * URLs, so the link names the run and the Web UI turns it into the run page.
 * The rewrite happens on the Markdown *source*, before it reaches `Markdown`:
 * the shared renderer's URL transform drops every scheme it does not know
 * (`run:` included, as it must for `javascript:`), so by the time a link is
 * rendered there is nothing left to rewrite.
 *
 * Handled forms:
 *
 * - `[text](run:name)` — `name` ends at whitespace or an unbalanced `)`, the
 *   CommonMark rules for a bare destination; `\)` escapes a parenthesis.
 * - `[text](<run:name with spaces>)` — the angle-bracket destination, for a
 *   run name a bare destination cannot carry.
 * - `[ref]: run:name` — a reference definition.
 *
 * In every form a `%XX` escape in the name is decoded, so `run:my%20run`
 * works too. Code is left alone: nothing inside a fenced block or an inline
 * code span is rewritten, so a notebook can still show the syntax itself.
 */

const SCHEME = "run:";

/** Percent-decode a run name, keeping it verbatim when it is not valid UTF-8 escapes. */
function decodeRunName(raw: string): string {
  try {
    return decodeURIComponent(raw);
  } catch {
    return raw;
  }
}

/**
 * Read a destination starting at `start` (just after `](` and any spaces).
 * Returns the run name and where the destination ends, or null when it is
 * not a `run:` destination.
 */
function readRunDestination(text: string, start: number): { name: string; end: number } | null {
  if (text.startsWith(`<${SCHEME}`, start)) {
    const close = text.indexOf(">", start);
    if (close === -1) return null;
    const raw = text.slice(start + 1 + SCHEME.length, close);
    if (raw.includes("<") || raw.includes("\n")) return null;
    return { name: decodeRunName(raw), end: close + 1 };
  }
  if (!text.startsWith(SCHEME, start)) return null;
  let i = start + SCHEME.length;
  let depth = 0;
  let raw = "";
  while (i < text.length) {
    const ch = text[i] as string;
    if (ch === "\\" && i + 1 < text.length && /[()\\]/.test(text[i + 1] as string)) {
      raw += text[i + 1];
      i += 2;
      continue;
    }
    if (/\s/.test(ch)) break;
    if (ch === "(") depth += 1;
    if (ch === ")") {
      if (depth === 0) break;
      depth -= 1;
    }
    raw += ch;
    i += 1;
  }
  return { name: decodeRunName(raw), end: i };
}

/** Rewrite inline `](run:…)` destinations in a stretch of non-code text. */
function rewriteInline(text: string, runHref: (name: string) => string): string {
  let out = "";
  let cursor = 0;
  let from = 0;
  for (;;) {
    const open = text.indexOf("](", from);
    if (open === -1) break;
    let start = open + 2;
    while (text[start] === " " || text[start] === "\t") start += 1;
    const dest = readRunDestination(text, start);
    if (dest === null || dest.name === "") {
      from = open + 2;
      continue;
    }
    // The angle-bracket form survives any character encodeURIComponent
    // leaves alone — parentheses included, which a bare destination would
    // have to balance.
    out += `${text.slice(cursor, start)}<${runHref(dest.name)}>`;
    cursor = dest.end;
    from = dest.end;
  }
  return out + text.slice(cursor);
}

/**
 * Rewrite one line outside a fenced block, skipping inline code spans. A span
 * opens with a run of N backticks and closes at the next run of exactly N; a
 * run with no partner is literal text, as CommonMark says.
 */
function rewriteLine(line: string, runHref: (name: string) => string): string {
  const definition = /^( {0,3}\[[^\]]+\]:[ \t]*)(<run:[^<>]*>|run:\S+)(.*)$/.exec(line);
  if (definition) {
    const [, head = "", dest = "", tail = ""] = definition;
    const parsed = readRunDestination(dest, 0);
    if (parsed && parsed.name !== "") return `${head}<${runHref(parsed.name)}>${tail}`;
  }

  let out = "";
  let text = "";
  let i = 0;
  while (i < line.length) {
    if (line[i] !== "`") {
      text += line[i];
      i += 1;
      continue;
    }
    let n = 0;
    while (line[i + n] === "`") n += 1;
    const fence = "`".repeat(n);
    let close = line.indexOf(fence, i + n);
    // Only a run of *exactly* n backticks closes the span.
    while (close !== -1 && (line[close + n] === "`" || line[close - 1] === "`")) {
      let skip = close;
      while (line[skip] === "`") skip += 1;
      close = line.indexOf(fence, skip);
    }
    if (close === -1) {
      text += fence;
      i += n;
      continue;
    }
    out += rewriteInline(text, runHref) + line.slice(i, close + n);
    text = "";
    i = close + n;
  }
  return out + rewriteInline(text, runHref);
}

/**
 * The notebook's Markdown with every `run:` link pointed at `runHref(name)`.
 * `runHref` is `expRunHref` bound to the project, so the run page URL is
 * built — and escaped — exactly the way every other run link is.
 */
export function rewriteRunLinks(source: string, runHref: (name: string) => string): string {
  if (!source.includes(SCHEME)) return source;
  const lines = source.split("\n");
  let fence: { char: string; len: number } | null = null;
  return lines
    .map((line) => {
      const marker = /^ {0,3}(`{3,}|~{3,})/.exec(line);
      if (fence) {
        if (
          marker?.[1]?.[0] === fence.char &&
          (marker[1]?.length ?? 0) >= fence.len &&
          line.trim() === marker[1]
        ) {
          fence = null;
        }
        return line;
      }
      if (marker?.[1]) {
        fence = { char: marker[1][0] as string, len: marker[1].length };
        return line;
      }
      return rewriteLine(line, runHref);
    })
    .join("\n");
}
