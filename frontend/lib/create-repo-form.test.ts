import { describe, expect, it } from "vitest";

import { createRepoFormAfterContextSwitch } from "@/lib/create-repo-form";

describe("createRepoFormAfterContextSwitch", () => {
  it("drops the previous namespace or kind's create error", () => {
    expect(createRepoFormAfterContextSwitch()).toEqual({ error: null });
  });
});
