import type { RawFileResponse } from "@/types/api";

/**
 * Turns a raw file response into the text to display.
 *
 * The backend base64-encodes any preview that is not valid UTF-8
 * (`handleRaw` in backend/internal/api/resolve.go). The obvious `atob(content)`
 * is wrong twice over: it throws on anything that is not well-formed base64,
 * which would take the whole page down with it, and it yields one JavaScript
 * character per *byte*, so multi-byte text comes out as mojibake.
 *
 * That path is easy to reach without a hostile file: previews are truncated at
 * 512KB, and a cut that lands in the middle of a multi-byte character makes an
 * ordinary UTF-8 document fail the server's `utf8.Valid` check, so it arrives
 * base64-encoded. Decoding the bytes with TextDecoder recovers the text and
 * leaves a single U+FFFD at the truncation point instead of garbling the file.
 */
export function decodeRawContent(
  raw: Pick<RawFileResponse, "content" | "encoding">,
): string | null {
  if (raw.encoding !== "base64") return raw.content;
  try {
    const binary = atob(raw.content);
    const bytes = Uint8Array.from(binary, (ch) => ch.charCodeAt(0));
    // Non-fatal on purpose: undecodable bytes become U+FFFD rather than
    // throwing away a preview that is mostly readable.
    return new TextDecoder("utf-8").decode(bytes);
  } catch {
    return null;
  }
}
