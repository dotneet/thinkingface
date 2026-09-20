import { describe, expect, it } from "vitest";

import { adminUserCreateFormAfterAdminToggle } from "@/lib/admin-user-create-form";

describe("adminUserCreateFormAfterAdminToggle", () => {
  it("drops the previous administrator-flag's create error", () => {
    expect(adminUserCreateFormAfterAdminToggle()).toEqual({ error: null });
  });
});
