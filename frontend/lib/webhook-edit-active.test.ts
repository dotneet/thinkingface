import { describe, expect, it } from "vitest";

import { seedWebhookEditActive, webhookActiveIsUnsaved } from "@/lib/webhook-edit-active";

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

describe("webhookActiveIsUnsaved", () => {
  it("does not treat a just-clicked Disable as an unsaved edit", () => {
    // Panel checkbox and committed write agree; webhook.active is still true.
    expect(webhookActiveIsUnsaved(false, false)).toBe(false);
  });

  it("does not treat a just-clicked Enable as an unsaved edit", () => {
    expect(webhookActiveIsUnsaved(true, true)).toBe(false);
  });

  it("flags a checkbox the user flipped after the last committed write", () => {
    expect(webhookActiveIsUnsaved(true, false)).toBe(true);
    expect(webhookActiveIsUnsaved(false, true)).toBe(true);
  });
});
