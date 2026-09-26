import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  listAllRepos,
  listFlagOn,
  listSearchTags,
  listTriState,
  nearestExistingDir,
  type RepoListSearch,
  repoListHref,
} from "@/lib/repos";
import type { RepoSummary } from "@/types/api";

describe("listFlagOn", () => {
  it("accepts the two spellings the backend accepts", () => {
    expect(listFlagOn("true")).toBe(true);
    expect(listFlagOn("1")).toBe(true);
  });

  it("treats anything else as off", () => {
    for (const value of ["false", "0", "yes", "", null, undefined]) {
      expect(listFlagOn(value)).toBe(false);
    }
  });
});

describe("listTriState", () => {
  it("keeps absent apart from false", () => {
    expect(listTriState(undefined)).toBeUndefined();
    expect(listTriState(null)).toBeUndefined();
    expect(listTriState("")).toBeUndefined();
    expect(listTriState("false")).toBe(false);
    expect(listTriState("true")).toBe(true);
  });
});

describe("listSearchTags", () => {
  it("merges the singular and plural spellings, deduplicated", () => {
    expect(listSearchTags({ tag: "nlp", tags: ["pytorch", "nlp"] })).toEqual(["pytorch", "nlp"]);
  });

  it("accepts a single repeated param as a bare string", () => {
    expect(listSearchTags({ tags: "nlp" })).toEqual(["nlp"]);
  });

  it("drops empty entries", () => {
    expect(listSearchTags({ tags: ["", "nlp"], tag: "" })).toEqual(["nlp"]);
  });
});

describe("repoListHref", () => {
  const href = (sp: RepoListSearch, overrides?: { offset?: number }) =>
    repoListHref("/models", sp, overrides);

  it("leaves a clean URL when nothing is filtered", () => {
    expect(href({})).toBe("/models");
    // Defaults never make it into the URL.
    expect(href({ sort: "updated", offset: "0" })).toBe("/models");
  });

  it("normalises the legacy q= and tag= spellings", () => {
    expect(href({ q: "bert", tag: "nlp" })).toBe("/models?search=bert&tags=nlp");
  });

  it("round-trips the lineage filters", () => {
    const url = new URL(
      href({ base_model: "alice/bert-base@main", relation: "quantized", dataset: "bob/imdb" }),
      "http://x",
    );
    expect(url.searchParams.get("base_model")).toBe("alice/bert-base@main");
    expect(url.searchParams.get("relation")).toBe("quantized");
    expect(url.searchParams.get("dataset")).toBe("bob/imdb");
  });

  it("emits base_only only when it is on", () => {
    expect(href({ base_only: "true" })).toBe("/models?base_only=true");
    // "1" is accepted on the way in but normalised on the way out.
    expect(href({ base_only: "1" })).toBe("/models?base_only=true");
    expect(href({ base_only: "false" })).toBe("/models");
  });

  it("keeps archived=false, which is a filter rather than a default", () => {
    expect(href({ archived: "false" })).toBe("/models?archived=false");
    expect(href({ archived: "true" })).toBe("/models?archived=true");
    expect(href({})).toBe("/models");
  });

  it("carries every filter across a page change", () => {
    const url = new URL(
      href({ search: "bert", tags: ["nlp"], base_model: "a/b", base_only: "true" }, { offset: 30 }),
      "http://x",
    );
    expect(url.searchParams.get("offset")).toBe("30");
    expect(url.searchParams.get("search")).toBe("bert");
    expect(url.searchParams.getAll("tags")).toEqual(["nlp"]);
    expect(url.searchParams.get("base_model")).toBe("a/b");
    expect(url.searchParams.get("base_only")).toBe("true");
  });

  it("lets an override reset the offset back to the first page", () => {
    expect(href({ offset: "60" }, { offset: 0 })).toBe("/models");
  });
});

describe("nearestExistingDir", () => {
  beforeEach(() => {
    vi.stubEnv("API_URL", "http://localhost:8080");
    vi.stubEnv("NEXT_PUBLIC_API_URL", "http://localhost:8080");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
  });

  function stubTree(existingPaths: string[]) {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        const parsed = new URL(url);
        const dir = parsed.pathname.replace(
          /^\/api\/v1\/repos\/model\/acme\/bert\/tree\/main\/?/,
          "",
        );
        if (existingPaths.includes(dir)) {
          return new Response(JSON.stringify({ path: dir, entries: [] }), { status: 200 });
        }
        return new Response(JSON.stringify({ error: { type: "not_found", message: "nope" } }), {
          status: 404,
        });
      }),
    );
  }

  it("returns the parent unchanged when it still exists", async () => {
    stubTree(["docs"]);
    const dest = await nearestExistingDir("model", "acme", "bert", "main", ["docs"]);
    expect(dest).toEqual(["docs"]);
  });

  it("walks up past a directory dropped for becoming empty", async () => {
    // "docs/guides" no longer exists (its last file was just deleted), but
    // "docs" itself still holds other content.
    stubTree(["docs"]);
    const dest = await nearestExistingDir("model", "acme", "bert", "main", ["docs", "guides"]);
    expect(dest).toEqual(["docs"]);
  });

  it("falls all the way back to the repo root when every ancestor is gone", async () => {
    stubTree([]);
    const dest = await nearestExistingDir("model", "acme", "bert", "main", ["docs", "guides"]);
    expect(dest).toEqual([]);
  });

  it("returns the root immediately for a top-level file, without a network call", async () => {
    const calls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        calls.push(url);
        return new Response("{}", { status: 200 });
      }),
    );
    const dest = await nearestExistingDir("model", "acme", "bert", "main", []);
    expect(dest).toEqual([]);
    expect(calls).toEqual([]);
  });
});

let fakeRepoId = 0;

function fakeRepo(name: string): RepoSummary {
  return {
    id: ++fakeRepoId,
    kind: "model",
    namespace: "acme",
    namespace_kind: "org",
    name,
    full_name: `acme/${name}`,
    description: "",
    tags: [],
    license: "",
    downloads: 0,
    total_size: 0,
    num_files: 0,
    is_experiment: false,
    default_branch: "main",
    head_sha: "",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    archived: false,
    archived_at: null,
  };
}

describe("listAllRepos", () => {
  beforeEach(() => {
    vi.stubEnv("API_URL", "http://localhost:8080");
    vi.stubEnv("NEXT_PUBLIC_API_URL", "http://localhost:8080");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
  });

  it("drops a repository a later page repeats (a push reordered the list mid-paging)", async () => {
    const all = Array.from({ length: 150 }, (_, i) => fakeRepo(`repo-${i}`));
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        const offset = Number(new URL(url).searchParams.get("offset") ?? 0);
        // The second page starts one early, repeating repo-99.
        const items = offset === 0 ? all.slice(0, 100) : all.slice(99, 150);
        return new Response(JSON.stringify({ items, total: all.length }), { status: 200 });
      }),
    );
    const result = await listAllRepos({ author: "acme" });
    expect(result.ok).toBe(true);
    if (!result.ok) throw new Error("expected success");
    expect(result.data.map((r) => r.name).filter((n) => n === "repo-99")).toHaveLength(1);
    expect(new Set(result.data.map((r) => r.id)).size).toBe(result.data.length);
  });

  it("pages past the server's 100-item ceiling instead of stopping at the first page", async () => {
    // 150 repos, split by the server into two 100-item-cap pages.
    const all = Array.from({ length: 150 }, (_, i) => fakeRepo(`repo-${i}`));
    const requests: number[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        const offset = Number(new URL(url).searchParams.get("offset") ?? 0);
        requests.push(offset);
        const items = all.slice(offset, offset + 100);
        return new Response(JSON.stringify({ items, total: all.length }), { status: 200 });
      }),
    );
    const result = await listAllRepos({ author: "acme" });
    expect(result.ok).toBe(true);
    if (!result.ok) throw new Error("expected success");
    expect(result.data).toHaveLength(150);
    expect(result.data[0]?.name).toBe("repo-0");
    expect(result.data[149]?.name).toBe("repo-149");
    expect(requests).toEqual([0, 100]);
  });

  it("stops after one page when everything already fit", async () => {
    const items = [fakeRepo("only-one")];
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify({ items, total: 1 }), { status: 200 })),
    );
    const result = await listAllRepos({ author: "acme" });
    expect(result.ok).toBe(true);
    if (!result.ok) throw new Error("expected success");
    expect(result.data).toHaveLength(1);
  });

  it("surfaces a failure on any page instead of returning a partial list silently", async () => {
    let call = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        call++;
        if (call === 1) {
          return new Response(
            JSON.stringify({
              items: Array.from({ length: 100 }, (_, i) => fakeRepo(`r${i}`)),
              total: 250,
            }),
            { status: 200 },
          );
        }
        return new Response(
          JSON.stringify({ error: { type: "internal_error", message: "boom" } }),
          {
            status: 500,
          },
        );
      }),
    );
    const result = await listAllRepos({ author: "acme" });
    expect(result.ok).toBe(false);
  });

  it("stops at maxItems rather than paging without bound", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              items: Array.from({ length: 100 }, (_, i) => fakeRepo(`r${i}`)),
              total: 100000,
            }),
            { status: 200 },
          ),
      ),
    );
    const result = await listAllRepos({ author: "acme" }, undefined, 250);
    expect(result.ok).toBe(true);
    if (!result.ok) throw new Error("expected success");
    // 3 pages of 100 each cross the 250 cap and the loop stops there, rather
    // than continuing toward the (bogus) 100000 total.
    expect(result.data).toHaveLength(300);
  });
});
