import { describe, expect, it } from "vitest";
import { countsArePartial, noPatchReason, parseUnifiedDiff } from "@/lib/diff";
import type { DiffFile } from "@/types/api";

function file(overrides: Partial<DiffFile> = {}): DiffFile {
  return {
    path: "a.txt",
    status: "modified",
    additions: 0,
    deletions: 0,
    binary: false,
    lfs: false,
    has_patch: true,
    no_patch_reason: "",
    patch: "",
    patch_truncated: false,
    old_size: 0,
    size: 0,
    ...overrides,
  };
}

describe("parseUnifiedDiff", () => {
  it("numbers each line against the side it exists on", () => {
    const lines = parseUnifiedDiff(
      ["@@ -1,3 +1,4 @@", " keep", "-gone", "+new", "+more", " tail"].join("\n"),
    );
    expect(lines.map((l) => [l.kind, l.text, l.oldLine, l.newLine])).toEqual([
      ["hunk", "@@ -1,3 +1,4 @@", null, null],
      ["context", "keep", 1, 1],
      ["del", "gone", 2, null],
      ["add", "new", null, 2],
      ["add", "more", null, 3],
      ["context", "tail", 3, 4],
    ]);
  });

  it("restarts the numbering at each hunk header", () => {
    const lines = parseUnifiedDiff(["@@ -1,1 +1,1 @@", " a", "@@ -40,1 +52,1 @@", " b"].join("\n"));
    const context = lines.filter((l) => l.kind === "context");
    expect(context.map((l) => [l.oldLine, l.newLine])).toEqual([
      [1, 1],
      [40, 52],
    ]);
  });

  it("accepts a hunk header without counts", () => {
    const lines = parseUnifiedDiff(["@@ -7 +9 @@", "-old", "+new"].join("\n"));
    expect(lines[0]?.kind).toBe("hunk");
    expect(lines[1]).toMatchObject({ kind: "del", oldLine: 7 });
    expect(lines[2]).toMatchObject({ kind: "add", newLine: 9 });
  });

  it("keeps an empty context line, and drops only the trailing newline's empty tail", () => {
    const lines = parseUnifiedDiff(["@@ -1,2 +1,2 @@", " a", "", " b", ""].join("\n"));
    expect(lines.map((l) => l.kind)).toEqual(["hunk", "context", "context", "context"]);
    expect(lines[2]).toMatchObject({ text: "", oldLine: 2, newLine: 2 });
  });

  it("does not let the no-newline marker advance either side", () => {
    const lines = parseUnifiedDiff(
      ["@@ -1,1 +1,1 @@", "-old", "\\ No newline at end of file", "+new"].join("\n"),
    );
    expect(lines[2]).toMatchObject({ kind: "meta", oldLine: null, newLine: null });
    expect(lines[3]).toMatchObject({ kind: "add", newLine: 1 });
  });

  it("shows an unnumbered line rather than dropping it when it precedes any hunk", () => {
    const lines = parseUnifiedDiff("+orphan");
    expect(lines).toEqual([{ kind: "add", text: "orphan", oldLine: null, newLine: null }]);
  });

  it("is empty for an empty patch", () => {
    expect(parseUnifiedDiff("")).toEqual([]);
  });
});

describe("noPatchReason", () => {
  it("is null when the file has a patch", () => {
    expect(noPatchReason(file({ has_patch: true }))).toBeNull();
  });

  it("reports the reason the backend stated", () => {
    expect(noPatchReason(file({ has_patch: false, no_patch_reason: "lfs", lfs: true }))).toBe(
      "lfs",
    );
    expect(noPatchReason(file({ has_patch: false, no_patch_reason: "binary", binary: true }))).toBe(
      "binary",
    );
    expect(noPatchReason(file({ has_patch: false, no_patch_reason: "too_large" }))).toBe(
      "tooLarge",
    );
    expect(noPatchReason(file({ has_patch: false, no_patch_reason: "unsupported" }))).toBe(
      "unsupported",
    );
  });

  // The regression this replaced: an empty file is neither binary nor LFS,
  // and the old rule concluded from that alone that it was too large to diff.
  it("does not call an empty file too large", () => {
    expect(
      noPatchReason(file({ has_patch: false, no_patch_reason: "no_text_change", size: 0 })),
    ).toBe("noTextChange");
  });

  it("falls back to noTextChange for a reason it does not know", () => {
    expect(noPatchReason(file({ has_patch: false, no_patch_reason: "something-new" }))).toBe(
      "noTextChange",
    );
  });
});

describe("countsArePartial", () => {
  it("is false only when every listed file was counted and none were dropped", () => {
    expect(countsArePartial([file(), file()], false)).toBe(false);
    expect(countsArePartial([file(), file({ has_patch: false, binary: true })], false)).toBe(true);
    expect(countsArePartial([file()], true)).toBe(true);
  });
});
