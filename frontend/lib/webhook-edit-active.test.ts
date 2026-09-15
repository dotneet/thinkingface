import { describe, expect, it } from "vitest";

import {
  seedWebhookEditActive,
  seedWebhookEditEvents,
  seedWebhookEditUrl,
  webhookActiveIsUnsaved,
  webhookEventsAreUnsaved,
  webhookUrlIsUnsaved,
} from "@/lib/webhook-edit-active";

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

describe("seedWebhookEditUrl", () => {
  it("keeps a just-saved URL when Edit reopens before the refetch", () => {
    expect(seedWebhookEditUrl("https://hooks.example/new", "https://hooks.example/old")).toBe(
      "https://hooks.example/new",
    );
  });

  it("follows the committed value once the refetch has landed", () => {
    expect(seedWebhookEditUrl("https://hooks.example/new", "https://hooks.example/new")).toBe(
      "https://hooks.example/new",
    );
  });
});

describe("webhookUrlIsUnsaved", () => {
  it("does not treat a just-saved URL as an unsaved edit", () => {
    expect(webhookUrlIsUnsaved("https://hooks.example/new", "https://hooks.example/new")).toBe(
      false,
    );
  });

  it("flags a URL the user retyped after the last committed write", () => {
    expect(webhookUrlIsUnsaved("https://hooks.example/draft", "https://hooks.example/new")).toBe(
      true,
    );
  });
});

describe("seedWebhookEditEvents", () => {
  it("keeps just-saved events when Edit reopens before the refetch", () => {
    expect(seedWebhookEditEvents(["repo.push"], ["repo.created", "repo.push"])).toEqual([
      "repo.push",
    ]);
  });
});

describe("webhookEventsAreUnsaved", () => {
  it("does not treat a just-saved event set as unsaved", () => {
    expect(webhookEventsAreUnsaved(new Set(["repo.push"]), new Set(["repo.push"]))).toBe(false);
  });

  it("ignores event order", () => {
    expect(
      webhookEventsAreUnsaved(
        new Set(["repo.push", "run.failed"]),
        new Set(["run.failed", "repo.push"]),
      ),
    ).toBe(false);
  });

  it("flags an event set the user changed after the last committed write", () => {
    expect(
      webhookEventsAreUnsaved(new Set(["repo.push", "run.failed"]), new Set(["repo.push"])),
    ).toBe(true);
  });
});
