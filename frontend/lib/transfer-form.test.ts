import { describe, expect, it } from "vitest";

import { transferFormAfterDestChange, transferFormAfterDestModeSwitch } from "@/lib/transfer-form";

describe("transferFormAfterDestModeSwitch", () => {
  it("drops the previous destination mode's submit error", () => {
    expect(transferFormAfterDestModeSwitch()).toEqual({ submitError: null });
  });
});

describe("transferFormAfterDestChange", () => {
  it("drops the previous destination namespace's submit error", () => {
    expect(transferFormAfterDestChange()).toEqual({ submitError: null });
  });
});
