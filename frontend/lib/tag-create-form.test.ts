import { describe, expect, it } from "vitest";

import { tagCreateFormAfterRevChange } from "@/lib/tag-create-form";

describe("tagCreateFormAfterRevChange", () => {
  it("drops the previous revision's create error", () => {
    expect(tagCreateFormAfterRevChange()).toEqual({ createError: null });
  });
});
