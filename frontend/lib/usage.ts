import { type ApiResult, apiFetch } from "@/lib/api";
import type { FetchOpts } from "@/lib/repos";
import type { UsageResponse } from "@/types/api";

export function getUsage(opts?: FetchOpts): Promise<ApiResult<UsageResponse>> {
  return apiFetch<UsageResponse>("/api/v1/usage", { headers: opts?.headers });
}

/**
 * Keeps only the rows belonging to `namespace`. `/api/v1/usage` answers with
 * every namespace the viewer can see, so an organisation's own storage screen
 * (components/settings/storage-usage.tsx) narrows it here rather than asking
 * the API for a slice it does not offer.
 *
 * The comparison is case-insensitive, like every other namespace lookup in
 * this system (`canCreateInNamespace` in lib/namespace.ts, the backend's
 * `LOWER(name)` matching, and the `/[ns]` route's redirect to the canonical
 * spelling). An exact match sent /orgs/ACME/settings/storage — a URL that
 * renders perfectly well — through the filter with nothing left, and the
 * screen then claimed the organisation had stored nothing (DESIGN.md §9).
 *
 * Pure and framework-free on purpose: check-ui.mjs's `client-boundary` rule
 * flags a plain value crossing out of a `"use client"` module, which is
 * exactly what this was before it moved here from storage-usage.tsx —
 * storage-usage.test.ts importing it from that file read as a Server
 * Component reading a client-only export.
 */
export function narrowToNamespace(usage: UsageResponse, namespace: string): UsageResponse {
  const target = namespace.toLowerCase();
  return {
    namespaces: usage.namespaces.filter((ns) => ns.namespace.toLowerCase() === target),
    repos: usage.repos.filter((repo) => repo.namespace.toLowerCase() === target),
  };
}

/**
 * True when a namespace-scoped view (an organisation's own storage settings
 * page) has nothing to show only because `/api/v1/usage` doesn't report
 * namespaces the caller isn't a member of -- never because the namespace is
 * genuinely empty. `namespaceRepoCount` is `Org.num_repos`, which counts
 * every repository regardless of visibility (backend/internal/store/orgs.go's
 * `orgRepoCount`), so a positive count that still lost every row here can
 * only mean the caller's membership, not the namespace's contents, is what
 * kept the data out.
 *
 * Exported so the distinction — the thing DESIGN.md §9 calls conflating
 * empty, zero and failure — can be unit tested without rendering the
 * component.
 */
export function isNamespaceUsageUnavailable(
  usage: UsageResponse,
  namespace: string | undefined,
  namespaceRepoCount: number | undefined,
): boolean {
  return (
    namespace !== undefined &&
    narrowToNamespace(usage, namespace).namespaces.length === 0 &&
    namespaceRepoCount !== undefined &&
    namespaceRepoCount > 0
  );
}
