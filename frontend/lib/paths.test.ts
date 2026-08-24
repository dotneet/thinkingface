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
  // Reading the resolved path is what most cases assert; anything not "ok"
  // fails the assertion with the state it actually got.
  const pathOf = (result: ReturnType<typeof resolveNewFilePath>) =>
    result.status === "ok" ? result.path : result.status;

  it("resolves a typed name against the directory being browsed", () => {
    expect(pathOf(resolveNewFilePath([], "notes.md"))).toBe("notes.md");
    expect(pathOf(resolveNewFilePath(["docs"], "notes.md"))).toBe("docs/notes.md");
    expect(pathOf(resolveNewFilePath(["docs", "guides"], "notes.md"))).toBe("docs/guides/notes.md");
  });

  it("keeps typed subdirectories below the browsed one", () => {
    expect(pathOf(resolveNewFilePath(["docs"], "guides/notes.md"))).toBe("docs/guides/notes.md");
  });

  it("treats a leading slash as a typo, not an escape to the root", () => {
    expect(pathOf(resolveNewFilePath(["docs"], "/notes.md"))).toBe("docs/notes.md");
    expect(pathOf(resolveNewFilePath(["docs"], "///notes.md"))).toBe("docs/notes.md");
  });

  it("collapses repeated and trailing separators", () => {
    expect(pathOf(resolveNewFilePath(["docs"], "guides//notes.md"))).toBe("docs/guides/notes.md");
    expect(pathOf(resolveNewFilePath([], "notes.md/"))).toBe("notes.md");
  });

  it("keeps dots that are part of a name rather than a whole segment", () => {
    expect(pathOf(resolveNewFilePath([], ".gitignore"))).toBe(".gitignore");
    expect(pathOf(resolveNewFilePath([], ".gitattributes"))).toBe(".gitattributes");
    expect(pathOf(resolveNewFilePath(["docs"], "..hidden.md"))).toBe("docs/..hidden.md");
    expect(pathOf(resolveNewFilePath(["docs"], "v1.2.3/notes.md"))).toBe("docs/v1.2.3/notes.md");
    expect(pathOf(resolveNewFilePath([], ".github/workflows/ci.yml"))).toBe(
      ".github/workflows/ci.yml",
    );
  });

  it("is empty — not invalid — for a blank entry, so nothing is typed yet", () => {
    expect(resolveNewFilePath([], "")).toEqual({ status: "empty" });
    expect(resolveNewFilePath(["docs"], "   ")).toEqual({ status: "empty" });
    expect(resolveNewFilePath(["docs"], "/")).toEqual({ status: "empty" });
  });

  // A ".." segment survives into the hint but not into the URL: the router
  // resolves "docs/../x.md" to a different file, so the dialog would show one
  // path and open another. Refused here, with a reason to render.
  it("refuses .. segments anywhere in the path", () => {
    for (const typed of ["../x.md", "a/../x.md", "a/b/../../x.md", "x/.."]) {
      expect(resolveNewFilePath(["docs"], typed), typed).toEqual({
        status: "invalid",
        issue: "relativeSegment",
      });
    }
  });

  it("refuses . segments anywhere in the path", () => {
    for (const typed of ["./x.md", "a/./x.md", "x/."]) {
      expect(resolveNewFilePath(["docs"], typed), typed).toEqual({
        status: "invalid",
        issue: "relativeSegment",
      });
    }
  });

  // Bare "." / ".." collapse the pushed URL down to something with no file
  // segment at all, which matches no route.
  it("refuses a bare . or ..", () => {
    expect(resolveNewFilePath(["docs"], ".")).toEqual({
      status: "invalid",
      issue: "relativeSegment",
    });
    expect(resolveNewFilePath(["docs"], "..")).toEqual({
      status: "invalid",
      issue: "relativeSegment",
    });
    expect(resolveNewFilePath([], "../..")).toEqual({
      status: "invalid",
      issue: "relativeSegment",
    });
  });

  // .git survives URL resolution intact, so without this the editor opens and
  // only the commit refuses -- a dead end found after typing a whole file.
  // git matches the name case-insensitively and so does this.
  it("refuses a .git segment in any case, at any depth", () => {
    for (const typed of [
      ".git/config",
      ".GIT/config",
      ".Git/hooks/pre-commit",
      "a/.git/config",
      "a/.gIt",
      ".git",
    ]) {
      expect(resolveNewFilePath(["docs"], typed), typed).toEqual({
        status: "invalid",
        issue: "gitDirectory",
      });
    }
  });

  it("allows a name that merely starts with .git", () => {
    expect(pathOf(resolveNewFilePath([], ".gitmodules"))).toBe(".gitmodules");
    expect(pathOf(resolveNewFilePath([], ".github"))).toBe(".github");
  });
});
