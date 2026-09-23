import { describe, expect, it } from "vitest";

import { createOrgFormAfterNameChange } from "@/lib/create-org-form";

describe("createOrgFormAfterNameChange", () => {
  it("drops the previous name's create error", () => {
    expect(createOrgFormAfterNameChange()).toEqual({ error: null });
  });
});
