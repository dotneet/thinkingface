/**
 * Client for the organisation endpoints (docs/dev/organization-design.md §7.1).
 *
 * Every function follows the `apiFetch` contract: it never throws, callers get
 * an `ApiResult<T>` and render an error state (CLAUDE.md invariant 3). The
 * ones a Server Component may call take `FetchOpts` so the `tf_session` cookie
 * can be forwarded with `authHeaders()` (invariant 2).
 */
import { type ApiResult, apiFetch } from "@/lib/api";
import type { FailedApiResult } from "@/lib/api-error-message";
import type { MessageKey } from "@/lib/i18n";
import type { FetchOpts } from "@/lib/repos";
import type {
  Org,
  OrgAuditLogResponse,
  OrgCreateRequest,
  OrgListResponse,
  OrgMemberAddRequest,
  OrgMemberResponse,
  OrgMembersResponse,
  OrgResponse,
  OrgRole,
  OrgUpdateRequest,
} from "@/types/api";

function orgPath(org: string, suffix = ""): string {
  return `/api/v1/orgs/${encodeURIComponent(org)}${suffix}`;
}

export type ListOrgsParams = {
  search?: string;
  limit?: number;
  offset?: number;
};

/** Public directory: every organisation, whether or not the viewer is a member. */
export function listOrgs(
  params: ListOrgsParams = {},
  opts?: FetchOpts,
): Promise<ApiResult<OrgListResponse>> {
  return apiFetch<OrgListResponse>("/api/v1/orgs", { query: params, headers: opts?.headers });
}

/** The signed-in user's own memberships, each with `viewer_role` filled in. */
export function listMyOrgs(opts?: FetchOpts): Promise<ApiResult<OrgListResponse>> {
  return apiFetch<OrgListResponse>("/api/v1/me/orgs", { headers: opts?.headers });
}

/**
 * One organisation. 200 for non-members too (only `viewer_role` and the
 * private repository counts differ); 404 when the name belongs to a user
 * namespace or to nothing at all.
 */
export function getOrg(name: string, opts?: FetchOpts): Promise<ApiResult<OrgResponse>> {
  return apiFetch<OrgResponse>(orgPath(name), { headers: opts?.headers });
}

export function createOrg(
  req: OrgCreateRequest,
  opts?: FetchOpts,
): Promise<ApiResult<OrgResponse>> {
  return apiFetch<OrgResponse>("/api/v1/orgs", {
    method: "POST",
    body: req,
    headers: opts?.headers,
  });
}

/** Partial update; admin only. Absent fields are left as they are. */
export function updateOrg(
  name: string,
  req: OrgUpdateRequest,
  opts?: FetchOpts,
): Promise<ApiResult<OrgResponse>> {
  return apiFetch<OrgResponse>(orgPath(name), {
    method: "PATCH",
    body: req,
    headers: opts?.headers,
  });
}

/** Admin only, and only while the organisation holds no repositories (409 otherwise). */
export function deleteOrg(name: string, opts?: FetchOpts): Promise<ApiResult<void>> {
  return apiFetch<void>(orgPath(name), { method: "DELETE", headers: opts?.headers });
}

export type ListMembersParams = {
  limit?: number;
  offset?: number;
};

/**
 * One page of the members. 403 for a non-member unless the organisation set
 * `members_visibility = "public"`, so callers treat a failure as "hidden"
 * rather than as an error.
 *
 * The window is clamped server-side to 200, and `total` counts the whole
 * membership regardless of it — so "is there another page?" is
 * `offset + items.length < total`, never a comparison against the `limit`
 * that was sent (docs/dev/api-contract.md §1.1).
 */
export function listMembers(
  name: string,
  params: ListMembersParams = {},
  opts?: FetchOpts,
): Promise<ApiResult<OrgMembersResponse>> {
  return apiFetch<OrgMembersResponse>(orgPath(name, "/members"), {
    query: params,
    headers: opts?.headers,
  });
}

export function addMember(
  name: string,
  req: OrgMemberAddRequest,
  opts?: FetchOpts,
): Promise<ApiResult<OrgMemberResponse>> {
  return apiFetch<OrgMemberResponse>(orgPath(name, "/members"), {
    method: "POST",
    body: req,
    headers: opts?.headers,
  });
}

export function updateMemberRole(
  name: string,
  username: string,
  role: OrgRole,
  opts?: FetchOpts,
): Promise<ApiResult<OrgMemberResponse>> {
  return apiFetch<OrgMemberResponse>(orgPath(name, `/members/${encodeURIComponent(username)}`), {
    method: "PATCH",
    body: { role },
    headers: opts?.headers,
  });
}

/** Removing yourself is how you leave; the last admin cannot (409 `last_admin`). */
export function removeMember(
  name: string,
  username: string,
  opts?: FetchOpts,
): Promise<ApiResult<void>> {
  return apiFetch<void>(orgPath(name, `/members/${encodeURIComponent(username)}`), {
    method: "DELETE",
    headers: opts?.headers,
  });
}

/** Admin only. `before` is the previous page's `next_before` cursor (0 = end). */
export function listAuditLog(
  name: string,
  params: { before?: number; limit?: number } = {},
  opts?: FetchOpts,
): Promise<ApiResult<OrgAuditLogResponse>> {
  return apiFetch<OrgAuditLogResponse>(orgPath(name, "/audit-log"), {
    // `before: 0` means "from the newest", which is also what omitting it
    // means — drop it so the first page never sends a meaningless cursor.
    query: { before: params.before || undefined, limit: params.limit },
    headers: opts?.headers,
  });
}

/**
 * The `error.type` values these endpoints answer with (§7.1), mapped to the
 * `org.errors.*` copy. Anything not listed here falls through to the status
 * fallbacks (and then the shared generic copy) in {@link orgErrorKey}, never
 * to the server's own message.
 */
const ERROR_KEYS: Record<string, MessageKey> = {
  org_creation_disabled: "org.errors.creationDisabled",
  reserved_name: "org.errors.nameReserved",
  last_admin: "org.errors.lastAdmin",
  has_repositories: "org.errors.hasRepositories",
  already_member: "org.errors.alreadyMember",
};

/**
 * Localizable message key for a failed organisation call. Always returns a
 * key — never null — so callers render `t(orgErrorKey(result))` directly
 * instead of falling back to the server's own message (which
 * `lib/api-error-message.ts` no longer shows on screen either).
 *
 * `fallbacks` lets a call site name what the ambiguous statuses mean in its
 * own context — a 404 from "add member" is a missing *user*, while a 404 from
 * "get org" is a missing organisation. Anything not covered here or by the
 * call site degrades to the shared generic copy (`errors.notFound` for a
 * missing thing, `errors.internalError` otherwise).
 */
export function orgErrorKey(
  result: FailedApiResult,
  fallbacks: Partial<Record<401 | 403 | 404, MessageKey>> = {},
): MessageKey {
  const byType = result.type ? ERROR_KEYS[result.type] : undefined;
  if (byType) return byType;
  if (result.status === 401) return fallbacks[401] ?? "org.errors.loginRequired";
  if (result.status === 403) return fallbacks[403] ?? "org.errors.permissionDenied";
  if (result.status === 404) return fallbacks[404] ?? "errors.notFound";
  return "errors.internalError";
}

/**
 * Base path of an organisation's admin area.
 *
 * The public page moved to `/{name}` (docs/dev/namespace-design.md §4.2), but the
 * settings screens deliberately stayed under `/orgs/{name}/settings`: the
 * backend's `/{ns}/{name}` is the git transport route, so `alice/settings` is
 * a legal repository path and cannot double as a settings URL.
 */
export function orgSettingsHref(name: string): string {
  return `/orgs/${encodeURIComponent(name)}/settings`;
}

/**
 * @deprecated Link to `namespaceHref(name)` (lib/namespace.ts) instead —
 * `/orgs/{name}` now permanently redirects there (docs/dev/namespace-design.md
 * §4.1). Kept because the `/orgs/{name}/settings/*` pages build their own
 * paths from it; use {@link orgSettingsHref} for those.
 */
export function orgHref(name: string): string {
  return `/orgs/${encodeURIComponent(name)}`;
}

/** True when the viewer may open this organisation's settings (§4). */
export function canAdminOrg(org: Org): boolean {
  return org.viewer_role === "admin";
}

/**
 * True when the viewer holds any role at all — a member (§4).
 *
 * `Org.viewer_role` is `""` for a non-member, but tygo renders `OrgRole` as
 * the union of its three named constants only, so the empty case has to be
 * compared through `string`. Same reason {@link isOrgRole} exists.
 */
export function isOrgMember(org: Org): boolean {
  return (org.viewer_role as string) !== "";
}

/** Every assignable role, in descending order of power (§4). */
export const ORG_ROLES: readonly OrgRole[] = ["admin", "write", "read"];

/** Narrows an arbitrary string (a `<select>` value) to a role. */
export function isOrgRole(value: string): value is OrgRole {
  return (ORG_ROLES as readonly string[]).includes(value);
}
