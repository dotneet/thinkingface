import { describe, expect, it } from "vitest";

import { namespaceEditorTargetAfterRowsChange } from "@/lib/admin-namespace-editor";

describe("namespaceEditorTargetAfterRowsChange", () => {
  it("closes the editor when its namespace has left the page", () => {
    expect(namespaceEditorTargetAfterRowsChange("acme", ["beta", "gamma"])).toBeNull();
  });

  it("keeps the editor when the namespace is still on the page", () => {
    expect(namespaceEditorTargetAfterRowsChange("acme", ["beta", "acme"])).toBe("acme");
  });

  it("leaves a closed editor closed", () => {
    expect(namespaceEditorTargetAfterRowsChange(null, ["acme"])).toBeNull();
  });
});
