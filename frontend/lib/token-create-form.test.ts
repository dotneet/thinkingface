import { describe, expect, it } from "vitest";

import { tokenCreateFormAfterContextSwitch } from "@/lib/token-create-form";

describe("tokenCreateFormAfterContextSwitch", () => {
  it("drops the previous scope or expiry's create error", () => {
    expect(tokenCreateFormAfterContextSwitch()).toEqual({ createError: null });
  });
});
