import { describe, expect, it } from "vitest";
import {
  cellFeatureFor,
  imageSourceFor,
  jsonTreeValueFor,
  parseJsonValue,
  prettyJson,
  sniffImageMime,
  stringifyValue,
} from "@/lib/cell-value";

/** Base64 of the given byte sequence, padded so it clears the "looks base64" length floor. */
function b64(bytes: number[], padTo = 24): string {
  const padded = [...bytes];
  while (padded.length < padTo) padded.push(0);
  return btoa(String.fromCharCode(...padded));
}

function ascii(text: string): number[] {
  return [...text].map((c) => c.charCodeAt(0));
}

const PNG = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a];
const JPEG = [0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46];
const GIF = ascii("GIF89a");
const BMP = ascii("BM");
// RIFF <4-byte size> WEBP
const WEBP = [...ascii("RIFF"), 0x1a, 0x00, 0x00, 0x00, ...ascii("WEBP"), ...ascii("VP8 ")];
const AVIF = [0x00, 0x00, 0x00, 0x20, ...ascii("ftypavif"), ...ascii("avifmif1")];
const HEIC = [0x00, 0x00, 0x00, 0x18, ...ascii("ftypheic"), ...ascii("heicmif1")];
const SVG = ascii('<svg xmlns="http://www.w3.org/2000/svg"/>');
const XML_SVG = ascii('<?xml version="1.0"?><svg/>');

describe("cellFeatureFor", () => {
  it("maps the HF feature and the JSON logical type", () => {
    expect(cellFeatureFor({ feature: "Image" })).toBe("image");
    expect(cellFeatureFor({ feature: "json" })).toBe("json");
    expect(cellFeatureFor({ logical_type: "JSON" })).toBe("json");
    expect(cellFeatureFor({ logical_type: "STRING" })).toBeUndefined();
    expect(cellFeatureFor({})).toBeUndefined();
  });
});

describe("sniffImageMime", () => {
  const cases: [string, number[], string][] = [
    ["PNG", PNG, "image/png"],
    ["JPEG", JPEG, "image/jpeg"],
    ["GIF", GIF, "image/gif"],
    ["WebP", WEBP, "image/webp"],
    ["BMP", BMP, "image/bmp"],
    ["AVIF", AVIF, "image/avif"],
    ["HEIC", HEIC, "image/heic"],
    ["SVG", SVG, "image/svg+xml"],
    ["XML-prefixed SVG", XML_SVG, "image/svg+xml"],
  ];
  for (const [name, bytes, mime] of cases) {
    it(`recognises ${name}`, () => {
      expect(sniffImageMime(Uint8Array.from(bytes))).toBe(mime);
    });
  }

  it("returns null for bytes it does not recognise", () => {
    expect(sniffImageMime(Uint8Array.from([1, 2, 3, 4, 5, 6, 7, 8]))).toBeNull();
    expect(sniffImageMime(Uint8Array.from([]))).toBeNull();
  });
});

describe("imageSourceFor", () => {
  it("reads the {bytes, path} struct and sniffs the MIME from the payload", () => {
    const src = imageSourceFor({ bytes: b64(PNG), path: "train/0.png" });
    expect(src?.src.startsWith("data:image/png;base64,")).toBe(true);
    expect(src?.path).toBe("train/0.png");
    expect(src?.bytes).toBe(24);
  });

  it("reads a bare base64 payload with no struct around it", () => {
    const src = imageSourceFor(b64(JPEG));
    expect(src?.src.startsWith("data:image/jpeg;base64,")).toBe(true);
    expect(src?.path).toBeNull();
  });

  it("falls back to the path extension when the bytes are unrecognisable", () => {
    const src = imageSourceFor({ bytes: b64([1, 2, 3, 4]), path: "a/b/pic.WEBP" });
    expect(src?.src.startsWith("data:image/webp;base64,")).toBe(true);
  });

  it("still produces a data: URL when nothing identifies the type", () => {
    const src = imageSourceFor({ bytes: b64([1, 2, 3, 4]), path: null });
    expect(src?.src.startsWith("data:application/octet-stream;base64,")).toBe(true);
  });

  it("uses path as the source when bytes are absent and path is a URL", () => {
    const src = imageSourceFor({ bytes: null, path: "https://example.com/a.png" });
    expect(src).toEqual({
      src: "https://example.com/a.png",
      path: "https://example.com/a.png",
      bytes: null,
    });
  });

  it("passes a data: URL through untouched", () => {
    const src = imageSourceFor("data:image/gif;base64,R0lGODlh");
    expect(src?.src).toBe("data:image/gif;base64,R0lGODlh");
    expect(src?.bytes).toBeNull();
  });

  it("normalises base64url payloads", () => {
    const src = imageSourceFor(b64(PNG).replace(/\+/g, "-").replace(/\//g, "_"));
    expect(src?.src.startsWith("data:image/png;base64,")).toBe(true);
  });

  it("rejects values that are plainly not images", () => {
    expect(imageSourceFor(42)).toBeNull();
    expect(imageSourceFor(null)).toBeNull();
    expect(imageSourceFor(undefined)).toBeNull();
    expect(imageSourceFor(true)).toBeNull();
    expect(imageSourceFor({ label: "cat" })).toBeNull();
    expect(imageSourceFor({ bytes: null, path: "train/0.png" })).toBeNull();
    expect(imageSourceFor("hello")).toBeNull();
    expect(imageSourceFor("not base64 at all, just a sentence")).toBeNull();
  });
});

describe("parseJsonValue", () => {
  it("passes objects and arrays through", () => {
    const obj = { a: 1 };
    expect(parseJsonValue(obj)).toBe(obj);
    expect(parseJsonValue([1, 2])).toEqual([1, 2]);
  });

  it("parses strings that look like JSON documents", () => {
    expect(parseJsonValue('  {"a": 1} ')).toEqual({ a: 1 });
    expect(parseJsonValue("[1,2,3]")).toEqual([1, 2, 3]);
  });

  it("returns undefined for scalars and unparseable strings", () => {
    expect(parseJsonValue("plain text")).toBeUndefined();
    expect(parseJsonValue("{not json")).toBeUndefined();
    expect(parseJsonValue("42")).toBeUndefined();
    expect(parseJsonValue(42)).toBeUndefined();
    expect(parseJsonValue(null)).toBeUndefined();
    expect(parseJsonValue(undefined)).toBeUndefined();
  });

  it("does not treat dates or binary views as JSON documents", () => {
    expect(parseJsonValue(new Date(0))).toBeUndefined();
    expect(parseJsonValue(new Uint8Array([1, 2]))).toBeUndefined();
  });
});

describe("jsonTreeValueFor", () => {
  it("only parses strings in a JSON column", () => {
    expect(jsonTreeValueFor('{"a":1}')).toBeUndefined();
    expect(jsonTreeValueFor('{"a":1}', "json")).toEqual({ a: 1 });
    expect(jsonTreeValueFor("not json", "json")).toBeUndefined();
  });

  it("always accepts objects and arrays", () => {
    expect(jsonTreeValueFor({ a: 1 })).toEqual({ a: 1 });
    expect(jsonTreeValueFor([1], "image")).toEqual([1]);
  });
});

describe("stringifyValue", () => {
  it("keeps the pre-existing rendering", () => {
    expect(stringifyValue(null)).toBe("null");
    expect(stringifyValue(undefined)).toBe("null");
    expect(stringifyValue("abc")).toBe("abc");
    expect(stringifyValue(7)).toBe("7");
    expect(stringifyValue({ a: 1 })).toBe('{"a":1}');
  });

  it("does not throw on a circular structure", () => {
    const circular: Record<string, unknown> = {};
    circular.self = circular;
    expect(() => stringifyValue(circular)).not.toThrow();
  });
});

describe("prettyJson", () => {
  it("indents by two spaces", () => {
    expect(prettyJson({ a: [1] })).toBe('{\n  "a": [\n    1\n  ]\n}');
  });

  it("renders BigInt instead of throwing", () => {
    expect(prettyJson({ n: 9007199254740993n })).toBe('{\n  "n": "9007199254740993"\n}');
  });

  it("degrades instead of throwing on a circular structure", () => {
    const circular: Record<string, unknown> = {};
    circular.self = circular;
    expect(() => prettyJson(circular)).not.toThrow();
  });
});
