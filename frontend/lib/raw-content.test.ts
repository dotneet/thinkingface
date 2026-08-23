import { describe, expect, it } from "vitest";
import { decodeRawContent } from "@/lib/raw-content";

function b64(bytes: number[]): string {
  return Buffer.from(Uint8Array.from(bytes)).toString("base64");
}

describe("decodeRawContent", () => {
  it("passes utf-8 payloads through untouched", () => {
    expect(decodeRawContent({ content: "# Title\n", encoding: "utf-8" })).toBe("# Title\n");
  });

  it("decodes base64 as utf-8 rather than one character per byte", () => {
    const content = Buffer.from("日本語テキスト", "utf8").toString("base64");
    expect(decodeRawContent({ content, encoding: "base64" })).toBe("日本語テキスト");
  });

  it("recovers a preview truncated in the middle of a multi-byte character", () => {
    // "あい" is 6 bytes; cutting after 4 leaves a dangling lead byte, which is
    // exactly what makes the server fall back to base64 for a plain text file.
    const full = Array.from(Buffer.from("あい", "utf8"));
    const decoded = decodeRawContent({ content: b64(full.slice(0, 4)), encoding: "base64" });
    expect(decoded).toBe("あ�");
  });

  it("substitutes rather than throwing on bytes that are not utf-8 at all", () => {
    const decoded = decodeRawContent({
      content: b64([0x82, 0xa0, 0x82, 0xa2]),
      encoding: "base64",
    });
    expect(decoded).not.toBeNull();
    expect(decoded).toContain("�");
  });

  it("returns null instead of throwing on malformed base64", () => {
    expect(decodeRawContent({ content: "not!valid!base64!", encoding: "base64" })).toBeNull();
  });

  it("handles an empty payload", () => {
    expect(decodeRawContent({ content: "", encoding: "base64" })).toBe("");
    expect(decodeRawContent({ content: "", encoding: "utf-8" })).toBe("");
  });
});
