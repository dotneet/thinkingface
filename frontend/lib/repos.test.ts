import { describe, expect, it } from "vitest";
import {
  listFlagOn,
  listSearchTags,
  listTriState,
  type RepoListSearch,
  repoListHref,
} from "@/lib/repos";

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
