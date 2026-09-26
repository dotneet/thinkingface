import { describe, expect, it } from "vitest";

import { rewriteRunLinks } from "@/lib/exp-notes";
import { expRunHref } from "@/lib/experiments";

const href = (name: string) => expRunHref("acme", "metrics", "proj", name);
const base = "/experiments/acme/metrics/proj";

describe("rewriteRunLinks", () => {
  it("leaves a notebook without run links untouched", () => {
    const src = "# Notes\n\n[docs](https://example.com) and `code`";
    expect(rewriteRunLinks(src, href)).toBe(src);
  });

  it("points a bare run: destination at the run page", () => {
    expect(rewriteRunLinks("see [best](run:lr-0.1) here", href)).toBe(
      `see [best](<${base}/lr-0.1>) here`,
    );
  });

  it("escapes the run name the way every other run link does", () => {
    expect(rewriteRunLinks("[a](run:sweep/lr=0.1)", href)).toBe(`[a](<${base}/sweep%2Flr%3D0.1>)`);
  });

  it("keeps balanced parentheses in a bare name and stops at the closing one", () => {
    expect(rewriteRunLinks("[a](run:bert(large)) tail", href)).toBe(
      `[a](<${base}/bert(large)>) tail`,
    );
  });

  it("honours backslash-escaped parentheses", () => {
    expect(rewriteRunLinks("[a](run:x\\)y)", href)).toBe(`[a](<${base}/x)y>)`);
  });

  it("keeps a link title after the destination", () => {
    expect(rewriteRunLinks('[a](run:r1 "the winner")', href)).toBe(
      `[a](<${base}/r1> "the winner")`,
    );
  });

  it("accepts the angle-bracket form for names with spaces", () => {
    expect(rewriteRunLinks("[a](<run:my run>)", href)).toBe(`[a](<${base}/my%20run>)`);
  });

  it("decodes percent escapes in the name", () => {
    expect(rewriteRunLinks("[a](run:my%20run)", href)).toBe(`[a](<${base}/my%20run>)`);
  });

  it("keeps a name with a stray % verbatim", () => {
    expect(rewriteRunLinks("[a](run:100%)", href)).toBe(`[a](<${base}/100%25>)`);
  });

  it("rewrites every link on a line", () => {
    expect(rewriteRunLinks("[a](run:x) vs [b](run:y)", href)).toBe(
      `[a](<${base}/x>) vs [b](<${base}/y>)`,
    );
  });

  it("rewrites reference definitions", () => {
    expect(rewriteRunLinks("[winner]\n\n[winner]: run:r7", href)).toBe(
      `[winner]\n\n[winner]: <${base}/r7>`,
    );
  });

  it("leaves an empty run: destination alone", () => {
    expect(rewriteRunLinks("[a](run:)", href)).toBe("[a](run:)");
  });

  it("does not touch inline code spans", () => {
    const src = "write `[text](run:name)` to link, e.g. [r](run:a)";
    expect(rewriteRunLinks(src, href)).toBe(
      `write \`[text](run:name)\` to link, e.g. [r](<${base}/a>)`,
    );
  });

  it("does not touch fenced code blocks", () => {
    const src = "```md\n[x](run:a)\n```\n[y](run:b)\n~~~\n[z](run:c)\n~~~";
    expect(rewriteRunLinks(src, href)).toBe(
      `\`\`\`md\n[x](run:a)\n\`\`\`\n[y](<${base}/b>)\n~~~\n[z](run:c)\n~~~`,
    );
  });

  it("treats an unmatched backtick as literal text", () => {
    expect(rewriteRunLinks("a ` b [r](run:x)", href)).toBe(`a \` b [r](<${base}/x>)`);
  });
});
