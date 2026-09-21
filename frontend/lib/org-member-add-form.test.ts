import { describe, expect, it } from "vitest";

import { orgMemberAddFormAfterUsernameChange } from "@/lib/org-member-add-form";

describe("orgMemberAddFormAfterUsernameChange", () => {
  it("drops the previous username's add error", () => {
    expect(orgMemberAddFormAfterUsernameChange()).toEqual({ addError: null });
  });
});
