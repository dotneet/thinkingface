import { describe, expect, it } from "vitest";

import { sshKeyCreateFormAfterFieldChange } from "@/lib/ssh-key-create-form";

describe("sshKeyCreateFormAfterFieldChange", () => {
  it("drops the previous title or key's add error", () => {
    expect(sshKeyCreateFormAfterFieldChange()).toEqual({ addError: null });
  });
});
