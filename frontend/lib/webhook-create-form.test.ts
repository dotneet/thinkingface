import { describe, expect, it } from "vitest";

import { webhookCreateFormAfterNamespaceSwitch } from "@/lib/webhook-create-form";

describe("webhookCreateFormAfterNamespaceSwitch", () => {
  it("drops the URL, events and repo scope from the previous namespace", () => {
    expect(webhookCreateFormAfterNamespaceSwitch()).toEqual({
      repoScope: "",
      url: "",
      events: [],
    });
  });
});
