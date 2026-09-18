import { describe, expect, it } from "vitest";

import { transferFormAfterDestModeSwitch } from "@/lib/transfer-form";

describe("transferFormAfterDestModeSwitch", () => {
  it("drops the previous destination mode's submit error", () => {
    expect(transferFormAfterDestModeSwitch()).toEqual({ submitError: null });
  });
});
