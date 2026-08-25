// Reading a commit's diff: the two questions the wire shape leaves to the UI.
//
// 1. A `patch` is a unified diff body with the `diff --git` / `index` /
//    `---` / `+++` headers already stripped (docs/dev/api-contract.md §2), so
//    it is hunks and nothing else. Turning that into rows the reader can
//    follow — which side a line belongs to, and what line number it carries —
//    is parsing, and parsing belongs in a testable module rather than inside
//    a component's render.
// 2. `additions` / `deletions` are only counts when `has_patch` is true. The
//    three reasons a patch can be missing (binary, LFS, skipped for size) are
//    not spelled out by a flag of their own — the size case is the *absence*
//    of the other two — so `noPatchReason` names it once instead of letting
//    every call site re-derive it and one of them get it wrong.

import type { DiffFile } from "@/types/api";

export type DiffLineKind = "add" | "del" | "context" | "hunk" | "meta";

export type DiffLine = {
  kind: DiffLineKind;
  /**
   * The line's content with the leading `+` / `-` / space marker removed. A
   * `hunk` or `meta` line keeps its text verbatim, marker included, because
   * the marker *is* the content there (`@@ -1,4 +1,6 @@`).
   */
  text: string;
  /** 1-based number on the old side, null on a line that only the new side has. */
  oldLine: number | null;
  /** 1-based number on the new side, null on a line that only the old side has. */
  newLine: number | null;
};

/**
 * `@@ -oldStart,oldCount +newStart,newCount @@ optional section heading`.
 * The counts are optional in the format (a one-line range omits them), and a
 * combined diff uses more than two `@`s — this never sees one, since the
 * backend diffs against the first parent only, but matching `@@+` costs
 * nothing and degrades to a labelled header rather than a mis-parsed row.
 */
const HUNK_HEADER = /^@@+ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/;

/**
 * Splits a unified diff body into rows, numbering each one against the side
 * it exists on.
 *
 * Line numbers only advance once a hunk header has said where the hunk
 * starts: content before the first header (which a well-formed patch does not
 * have) is passed through as `meta` rather than being numbered from a guess.
 */
export function parseUnifiedDiff(patch: string): DiffLine[] {
  if (patch === "") return [];
  const raw = patch.split("\n");
  // A patch that ends in a newline splits into a final empty element that is
  // not a line of the diff. Only the last one is dropped: an empty string
  // *inside* the body is a real context line whose content is empty.
  if (raw.length > 0 && raw[raw.length - 1] === "") raw.pop();

  const lines: DiffLine[] = [];
  let oldLine = 0;
  let newLine = 0;
  let numbering = false;

  for (const line of raw) {
    const hunk = HUNK_HEADER.exec(line);
    if (hunk) {
      oldLine = Number(hunk[1]);
      newLine = Number(hunk[2]);
      numbering = true;
      lines.push({ kind: "hunk", text: line, oldLine: null, newLine: null });
      continue;
    }
    const marker = line[0] ?? " ";
    if (marker === "+") {
      lines.push({
        kind: "add",
        text: line.slice(1),
        oldLine: null,
        newLine: numbering ? newLine++ : null,
      });
    } else if (marker === "-") {
      lines.push({
        kind: "del",
        text: line.slice(1),
        oldLine: numbering ? oldLine++ : null,
        newLine: null,
      });
    } else if (marker === "\\") {
      // `\ No newline at end of file` — a note about the line above, not a
      // line of either side, so it advances neither counter.
      lines.push({ kind: "meta", text: line, oldLine: null, newLine: null });
    } else if (marker === " " || line === "") {
      lines.push({
        kind: "context",
        text: line.slice(1),
        oldLine: numbering ? oldLine++ : null,
        newLine: numbering ? newLine++ : null,
      });
    } else {
      // Nothing else is legal in a body with the headers stripped. Show it
      // rather than dropping it: a line the reader can see is a bug report,
      // a line silently swallowed is a diff that quietly lies.
      lines.push({ kind: "meta", text: line, oldLine: null, newLine: null });
    }
  }
  return lines;
}

/** Why a file in a commit diff carries no patch. */
export type NoPatchReason =
  | "binary"
  | "lfs"
  | "tooLarge"
  | "noTextChange"
  | "unsupported"
  | "budgetSpent";

/**
 * The reason `file` has no unified diff, or null when it has one.
 *
 * The backend states the reason (`no_patch_reason`); this only maps it to the
 * dictionary's spelling. It used to be *inferred* — "not binary and not LFS,
 * therefore it must have been too big" — which reported an empty file, and a
 * mode-only change, as too large to diff.
 *
 * An unrecognised reason falls back to `noTextChange`, the one answer that is
 * true of every patchless file: whatever else happened, there are no lines to
 * show. Guessing `tooLarge` there would put back the exact bug this replaced.
 */
export function noPatchReason(file: DiffFile): NoPatchReason | null {
  if (file.has_patch) return null;
  switch (file.no_patch_reason) {
    case "lfs":
      return "lfs";
    case "binary":
      return "binary";
    case "too_large":
      return "tooLarge";
    case "unsupported":
      return "unsupported";
    case "budget_spent":
      return "budgetSpent";
    default:
      return "noTextChange";
  }
}

/**
 * True when the response's `additions` / `deletions` totals cover less than
 * the whole commit — because a listed file had no patch to count, or because
 * the file list itself was capped. The totals stay honest numbers either way;
 * this is what says they are a floor rather than the answer.
 */
export function countsArePartial(files: DiffFile[], filesTruncated: boolean): boolean {
  return filesTruncated || files.some((file) => !file.has_patch);
}
