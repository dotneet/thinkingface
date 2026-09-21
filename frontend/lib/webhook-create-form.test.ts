import { describe, expect, it } from "vitest";

import {
  webhookCreateFormAfterEventsChange,
  webhookCreateFormAfterNamespaceSwitch,
  webhookCreateFormAfterRepoScopeChange,
} from "@/lib/webhook-create-form";

describe("webhookCreateFormAfterNamespaceSwitch", () => {
  it("drops the URL, events and repo scope from the previous namespace", () => {
    expect(webhookCreateFormAfterNamespaceSwitch()).toEqual({
      repoScope: "",
      url: "",
      events: [],
    });
  });
});

describe("webhookCreateFormAfterRepoScopeChange", () => {
  it("drops the previous repository scope's create error", () => {
    expect(webhookCreateFormAfterRepoScopeChange()).toEqual({ createError: null });
  });
});

describe("webhookCreateFormAfterEventsChange", () => {
  it("drops the previous event set's create error", () => {
    expect(webhookCreateFormAfterEventsChange()).toEqual({ createError: null });
  });
});
