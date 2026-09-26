import { describe, expect, it } from "vitest";

import { isNamespaceUsageUnavailable, narrowToNamespace } from "@/lib/usage";
import type { UsageResponse } from "@/types/api";

function usage(overrides: Partial<UsageResponse> = {}): UsageResponse {
  return { namespaces: [], repos: [], ...overrides };
}

function ns(namespace: string, overrides: Partial<UsageResponse["namespaces"][number]> = {}) {
  return {
    namespace,
    lfs_size: 0,
    num_files: 0,
    num_repos: 1,
    effective_quota_bytes: null,
    ...overrides,
  };
}

describe("narrowToNamespace", () => {
  it("keeps only the rows for the requested namespace, case-insensitively", () => {
    const data = usage({ namespaces: [ns("acme"), ns("other")] });
    expect(narrowToNamespace(data, "ACME").namespaces).toEqual([ns("acme")]);
  });

  it("returns an empty result when the namespace has no row at all", () => {
    const data = usage({ namespaces: [ns("other")] });
    expect(narrowToNamespace(data, "acme").namespaces).toEqual([]);
  });
});

// DESIGN.md §9: /api/v1/usage only reports namespaces the caller is a member
// of (backend/internal/api/usage.go), so a site admin opening an
// organisation's storage settings without being a member sees the same empty
// response a genuinely-empty organisation would produce. isNamespaceUsageUnavailable
// is what tells the two apart, using Org.num_repos (which counts every
// repository "regardless of visibility", per orgRepoCount's own comment) as
// the signal that the namespace is not actually empty.
describe("isNamespaceUsageUnavailable", () => {
  it("is false for the unscoped view (no namespace requested at all)", () => {
    expect(isNamespaceUsageUnavailable(usage(), undefined, 5)).toBe(false);
  });

  it("is false once the namespace has a row (it is a member, or repoCount is unknown but the row is there)", () => {
    const data = usage({ namespaces: [ns("acme")] });
    expect(isNamespaceUsageUnavailable(data, "acme", 3)).toBe(false);
  });

  it("is false for a namespace that genuinely owns no repositories", () => {
    // No row for "acme", but its own num_repos says there is nothing to hide.
    expect(isNamespaceUsageUnavailable(usage(), "acme", 0)).toBe(false);
  });

  it("is false when the repo count is unknown -- never assume unavailable without the signal", () => {
    expect(isNamespaceUsageUnavailable(usage(), "acme", undefined)).toBe(false);
  });

  it("is true for a non-empty organisation missing from the response (the bug)", () => {
    // A 50 GB organisation a site admin isn't a member of: it owns
    // repositories (num_repos > 0) but /api/v1/usage left it out entirely.
    expect(isNamespaceUsageUnavailable(usage(), "acme", 12)).toBe(true);
  });

  it("still applies after another namespace's row is present", () => {
    const data = usage({ namespaces: [ns("other")] });
    expect(isNamespaceUsageUnavailable(data, "acme", 1)).toBe(true);
  });
});
