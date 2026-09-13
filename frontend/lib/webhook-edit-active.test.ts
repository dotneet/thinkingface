import { describe, expect, it } from "vitest";

import { seedWebhookEditActive } from "@/lib/webhook-edit-active";

describe("seedWebhookEditActive", () => {
  it("keeps a just-disabled webhook disabled when Edit opens before the refetch", () => {
    // Enable/Disable wrote false locally; webhook.active is still true.
    expect(seedWebhookEditActive(false, true)).toBe(false);
  });

  it("keeps a just-enabled webhook enabled when Edit opens before the refetch", () => {
    expect(seedWebhookEditActive(true, false)).toBe(true);
  });

  it("follows the committed value once the refetch has landed", () => {
    expect(seedWebhookEditActive(false, false)).toBe(false);
    expect(seedWebhookEditActive(true, true)).toBe(true);
  });
});
