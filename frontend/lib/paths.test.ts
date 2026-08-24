import { afterEach, describe, expect, it, vi } from "vitest";
import {
  encodePathSegments,
  publicApiBase,
  repoBase,
  repoBlobHref,
  repoEditHref,
  repoTreeHref,
  repoViewerHref,
  resolveNewFilePath,
} from "@/lib/paths";

describe("publicApiBase", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("falls back to the local dev backend", () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", undefined as unknown as string);
    expect(publicApiBase()).toBe("http://localhost:8080");
  });

  it("prefers the configured public URL", () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", "https://api.example.com");
    expect(publicApiBase()).toBe("https://api.example.com");
  });
});

describe("encodePathSegments", () => {
  it("returns an empty string for empty or slash-only input", () => {
    expect(encodePathSegments("")).toBe("");
    expect(encodePathSegments("/")).toBe("");
    expect(encodePathSegments("///")).toBe("");
  });

  it("drops leading, trailing, and repeated separators", () => {
    expect(encodePathSegments("/a//b/")).toBe("a/b");
  });

  it("encodes each segment but keeps the separators literal", () => {
    expect(encodePathSegments("data files/train set.csv")).toBe("data%20files/train%20set.csv");
    expect(encodePathSegments("a#b/c?d")).toBe("a%23b/c%3Fd");
  });

  it("does not resolve dot segments", () => {
    expect(encodePathSegments("a/../b")).toBe("a/../b");
  });
});

describe("repoBase", () => {
  it("pluralises the kind and encodes namespace and name", () => {
    expect(repoBase("model", "acme", "bert")).toBe("/models/acme/bert");
    expect(repoBase("dataset", "acme", "bert")).toBe("/datasets/acme/bert");
    expect(repoBase("model", "a b", "c/d")).toBe("/models/a%20b/c%2Fd");
  });
});

describe("repoTreeHref", () => {
  it("omits the path suffix at the repository root", () => {
    expect(repoTreeHref("model", "acme", "bert", "main")).toBe("/models/acme/bert/tree/main");
    expect(repoTreeHref("model", "acme", "bert", "main", "")).toBe("/models/acme/bert/tree/main");
  });

  it("appends encoded path segments", () => {
    expect(repoTreeHref("dataset", "acme", "bert", "main", "a/b c")).toBe(
      "/datasets/acme/bert/tree/main/a/b%20c",
    );
  });

  it("encodes the revision as a single segment", () => {
    expect(repoTreeHref("model", "acme", "bert", "feature/x")).toBe(
      "/models/acme/bert/tree/feature%2Fx",
    );
  });
});

describe("blob / edit / viewer hrefs", () => {
  const args = ["model", "acme", "bert", "main", "sub dir/file.txt"] as const;

  it("differ only by their route segment", () => {
    expect(repoBlobHref(...args)).toBe("/models/acme/bert/blob/main/sub%20dir/file.txt");
    expect(repoEditHref(...args)).toBe("/models/acme/bert/edit/main/sub%20dir/file.txt");
    expect(repoViewerHref(...args)).toBe("/models/acme/bert/viewer/main/sub%20dir/file.txt");
  });

  it("collapses an empty path into a trailing separator", () => {
    expect(repoBlobHref("model", "acme", "bert", "main", "")).toBe("/models/acme/bert/blob/main/");
  });
});

describe("resolveNewFilePath", () => {
  it("resolves a typed name against the directory being browsed", () => {
    expect(resolveNewFilePath([], "notes.md")).toBe("notes.md");
    expect(resolveNewFilePath(["docs"], "notes.md")).toBe("docs/notes.md");
    expect(resolveNewFilePath(["docs", "guides"], "notes.md")).toBe("docs/guides/notes.md");
  });

  it("keeps typed subdirectories below the browsed one", () => {
    expect(resolveNewFilePath(["docs"], "guides/notes.md")).toBe("docs/guides/notes.md");
  });

  it("treats a leading slash as a typo, not an escape to the root", () => {
    expect(resolveNewFilePath(["docs"], "/notes.md")).toBe("docs/notes.md");
    expect(resolveNewFilePath(["docs"], "///notes.md")).toBe("docs/notes.md");
  });

  it("collapses repeated and trailing separators", () => {
    expect(resolveNewFilePath(["docs"], "guides//notes.md")).toBe("docs/guides/notes.md");
    expect(resolveNewFilePath([], "notes.md/")).toBe("notes.md");
  });

  it("is empty for a blank entry, so the dialog can keep Create disabled", () => {
    expect(resolveNewFilePath([], "")).toBe("");
    expect(resolveNewFilePath(["docs"], "   ")).toBe("");
    expect(resolveNewFilePath(["docs"], "/")).toBe("");
  });
});
