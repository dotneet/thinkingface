import { describe, expect, it } from "vitest";

import { defaultBranchFormAfterSelectionChange } from "@/lib/default-branch-form";

describe("defaultBranchFormAfterSelectionChange", () => {
  it("drops the previous selection's save error", () => {
    expect(defaultBranchFormAfterSelectionChange()).toEqual({ error: null });
  });
});
