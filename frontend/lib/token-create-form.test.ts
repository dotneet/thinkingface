import { describe, expect, it } from "vitest";

import { parseTokenRepos, tokenCreateFormAfterContextSwitch } from "@/lib/token-create-form";

describe("tokenCreateFormAfterContextSwitch", () => {
  it("drops the previous scope or expiry's create error", () => {
    expect(tokenCreateFormAfterContextSwitch()).toEqual({ createError: null });
  });
});

describe("parseTokenRepos", () => {
  it("is empty for an empty or blank field", () => {
    expect(parseTokenRepos("")).toEqual([]);
    expect(parseTokenRepos("  \n\n \t\n")).toEqual([]);
  });

  it("takes one entry per line, trimmed, without repeats", () => {
    expect(
      parseTokenRepos(" datasets/alice/exp \r\nmodels/alice/ocr\n\ndatasets/alice/exp\n"),
    ).toEqual(["datasets/alice/exp", "models/alice/ocr"]);
  });

  it("leaves the spelling for the server to judge", () => {
    expect(parseTokenRepos("alice/exp")).toEqual(["alice/exp"]);
  });
});
