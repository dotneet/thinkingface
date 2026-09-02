import { readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  loginHref,
  RESERVED_NAMESPACE_NAMES,
  safeRedirectPath,
  validateName,
  validateNamespaceName,
} from "@/lib/validation";

describe("validateName", () => {
  it("accepts the shapes the server accepts", () => {
    for (const name of [
      "a",
      "A1",
      "my-dataset",
      "my_dataset",
      "bert.base",
      "9lives",
      "a".repeat(96),
    ]) {
      expect(validateName(name), name).toBeNull();
    }
  });

  it("rejects an empty name", () => {
    expect(validateName("")).toBe("required");
  });

  it("rejects names that do not start with a letter or digit", () => {
    for (const name of ["-lead", "_lead", ".lead"]) {
      expect(validateName(name), name).toBe("invalid");
    }
  });

  it("rejects characters outside the allowed set", () => {
    for (const name of ["my dataset", "my/dataset", "データ", "a+b", "a@b", "a:b"]) {
      expect(validateName(name), name).toBe("invalid");
    }
  });

  it("rejects names longer than 96 characters", () => {
    expect(validateName("a".repeat(97))).toBe("invalid");
  });

  it("rejects a .git suffix, which the regex alone would allow", () => {
    // "repo.git" matches the character class, so this is a separate rule on
    // both sides of the wire.
    expect(validateName("repo.git")).toBe("gitSuffix");
  });
});

describe("safeRedirectPath", () => {
  it("keeps ordinary same-origin paths", () => {
    expect(safeRedirectPath("/settings/tokens")).toBe("/settings/tokens");
    expect(safeRedirectPath("/datasets?q=bert#top")).toBe("/datasets?q=bert#top");
  });

  it("falls back when there is nothing to redirect to", () => {
    expect(safeRedirectPath(undefined)).toBe("/");
    expect(safeRedirectPath(null)).toBe("/");
    expect(safeRedirectPath("")).toBe("/");
    expect(safeRedirectPath("", "/new")).toBe("/new");
  });

  it("rejects absolute URLs to another origin", () => {
    expect(safeRedirectPath("https://evil.example/login")).toBe("/");
    expect(safeRedirectPath("http://evil.example")).toBe("/");
  });

  it("rejects protocol-relative URLs", () => {
    expect(safeRedirectPath("//evil.example/login")).toBe("/");
  });

  it("rejects backslash forms that the URL parser reads as an authority", () => {
    // The WHATWG parser treats "\" like "/", so these resolve to evil.example
    // even though they start with a single forward slash.
    expect(safeRedirectPath("/\\evil.example")).toBe("/");
    expect(safeRedirectPath("/\\/evil.example")).toBe("/");
    expect(safeRedirectPath("\\\\evil.example")).toBe("/");
  });

  it("rejects a relative path that could escape the origin", () => {
    expect(safeRedirectPath("javascript:alert(1)")).toBe("/");
    expect(safeRedirectPath("settings/tokens")).toBe("/settings/tokens");
  });

  it("strips a smuggled host but keeps a genuinely same-origin path", () => {
    expect(safeRedirectPath("/a/../b")).toBe("/b");
  });
});

describe("validateNamespaceName", () => {
  it("accepts an ordinary namespace name", () => {
    for (const name of ["alice", "acme-research", "team_1", "a9"]) {
      expect(validateNamespaceName(name), name).toBeNull();
    }
  });

  it("still applies every validateName rule", () => {
    expect(validateNamespaceName("")).toBe("required");
    expect(validateNamespaceName("-lead")).toBe("invalid");
    expect(validateNamespaceName("repo.git")).toBe("gitSuffix");
  });

  it("rejects every name that would collide with a route", () => {
    // The whole list matters: each entry shadows a front-end route, a
    // backend /{ns}/{name} route, or the HF-compatible /datasets prefix.
    // "_next" is caught one step earlier — the syntax rule already bans a
    // leading underscore — so only the code differs, never the outcome.
    for (const name of RESERVED_NAMESPACE_NAMES) {
      const expected = validateName(name) ?? "reserved";
      expect(validateNamespaceName(name), name).toBe(expected);
      expect(validateNamespaceName(name), name).not.toBeNull();
    }
  });

  it("matches reserved names case-insensitively", () => {
    expect(validateNamespaceName("Settings")).toBe("reserved");
    expect(validateNamespaceName("DATASETS")).toBe("reserved");
  });

  it("does not reject a name that merely contains a reserved word", () => {
    expect(validateNamespaceName("settings-team")).toBeNull();
    expect(validateNamespaceName("my-models")).toBeNull();
  });

  it("covers the names the design lists (docs/dev/organization-design.md §6.3)", () => {
    // Guards against an entry being dropped while syncing with
    // backend/internal/api/names.go.
    expect(RESERVED_NAMESPACE_NAMES).toContain("orgs");
    expect(RESERVED_NAMESPACE_NAMES).toContain("_next");
    expect(RESERVED_NAMESPACE_NAMES).toContain("whoami-v2");
    expect(new Set(RESERVED_NAMESPACE_NAMES).size).toBe(RESERVED_NAMESPACE_NAMES.length);
  });

  it("reserves every static top-level route under app/ (docs/dev/namespace-design.md §9)", () => {
    // `/[ns]` is a Next.js dynamic segment matched only after every static
    // route under app/ has missed, so any static directory that is *not* on
    // the reserved list would shadow a namespace of the same name with no
    // warning at build time. This mirrors the "everything directly under app/
    // is in the list" half of the machine check frontend/scripts/check-ui.mjs
    // runs in CI; run it here too so the sync is caught by `bun run test` as
    // well as `check:ui`.
    const appDir = fileURLToPath(new URL("../app", import.meta.url));
    const staticSegments = readdirSync(appDir, { withFileTypes: true })
      // Same exclusions as check-ui.mjs: dynamic segments and route groups
      // never occupy a URL segment of their own.
      .filter(
        (entry) =>
          entry.isDirectory() && !entry.name.startsWith("[") && !entry.name.startsWith("("),
      )
      .map((entry) => entry.name.toLowerCase());

    // Sanity check that the walk actually found real routes, so a refactor
    // that silently pointed this at an empty directory would fail loudly
    // instead of vacuously passing.
    expect(staticSegments.length).toBeGreaterThan(0);

    const missing = staticSegments.filter((name) => !RESERVED_NAMESPACE_NAMES.includes(name));
    expect(missing, `app/ route(s) not in RESERVED_NAMESPACE_NAMES: ${missing.join(", ")}`).toEqual(
      [],
    );
  });
});

describe("loginHref", () => {
  it("carries the query string of the page the user is on", () => {
    // The header's "Log in" link built this from usePathname() alone, which
    // has no search params: someone reading a filtered listing came back to
    // an unfiltered one after signing in.
    expect(loginHref("/datasets", "search=bert&tags=nlp&sort=downloads")).toBe(
      "/login?next=%2Fdatasets%3Fsearch%3Dbert%26tags%3Dnlp%26sort%3Ddownloads",
    );
  });

  it("accepts useSearchParams().toString() with or without a leading ?", () => {
    expect(loginHref("/models", "?search=bert")).toBe(loginHref("/models", "search=bert"));
  });

  it("omits next= when there is no query string", () => {
    expect(loginHref("/settings/tokens")).toBe("/login?next=%2Fsettings%2Ftokens");
    expect(loginHref("/settings/tokens", "")).toBe("/login?next=%2Fsettings%2Ftokens");
    expect(loginHref("/settings/tokens", null)).toBe("/login?next=%2Fsettings%2Ftokens");
  });

  it("never points /login at itself, or at a pathname the router has not resolved", () => {
    expect(loginHref("/login", "next=%2Fdatasets")).toBe("/login");
    expect(loginHref(null)).toBe("/login");
    expect(loginHref("")).toBe("/login");
  });

  it("round-trips through safeRedirectPath with the query intact", () => {
    const href = loginHref("/datasets", "search=bert&tags=nlp");
    const next = new URL(href, "http://redirect.invalid").searchParams.get("next");
    expect(safeRedirectPath(next)).toBe("/datasets?search=bert&tags=nlp");
  });

  it("cannot smuggle another origin into next=", () => {
    // Defence in depth: the value is caller-controlled only through the
    // router, but safeRedirectPath is what actually enforces this.
    const href = loginHref("//evil.example/path");
    const next = new URL(href, "http://redirect.invalid").searchParams.get("next");
    expect(safeRedirectPath(next)).toBe("/");
  });
});
