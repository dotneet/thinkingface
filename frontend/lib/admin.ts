/**
 * Account credentials and site administration
 * (docs/dev/api-contract.md §1.3).
 *
 * Two audiences in one module: `changeMyPassword` is what any signed-in user
 * calls from /settings/account, and the `adminUsers*` functions are the site
 * administrator's account directory behind /settings/admin/users.
 *
 * Every function follows the `apiFetch` contract — it never throws, callers
 * get an `ApiResult<T>` and render an error state (CLAUDE.md invariant 3).
 * The ones a Server Component may call take `FetchOpts` so the `tf_session`
 * cookie can be forwarded with `authHeaders()` (invariant 2).
 */
import type { ApiResult } from "@/lib/api";
import { apiFetch } from "@/lib/api";
import type { MessageKey } from "@/lib/i18n";
import type { FetchOpts } from "@/lib/repos";
import type {
  AdminUserCreateRequest,
  AdminUserListResponse,
  AdminUserResponse,
  AdminUserUpdateRequest,
  PasswordChangeRequest,
} from "@/types/api";

/**
 * Replace your own password. 204 on success, 401 when `current_password` is
 * wrong — which is a different sentence from "log in first", so call sites
 * pass their own copy for that status rather than using `errors.unauthorized`.
 *
 * A browser caller keeps its session: the backend re-issues the `tf_session`
 * cookie at the new epoch while revoking every other one. Access tokens are
 * deliberately untouched.
 */
export function changeMyPassword(req: PasswordChangeRequest): Promise<ApiResult<void>> {
  return apiFetch<void>("/api/v1/me/password", { method: "PATCH", body: req });
}

export type AdminUsersParams = {
  search?: string;
  limit?: number;
  offset?: number;
};

/** The account directory. Site administrators only; 403 for anyone else. */
export function listAdminUsers(
  params: AdminUsersParams = {},
  opts?: FetchOpts,
): Promise<ApiResult<AdminUserListResponse>> {
  return apiFetch<AdminUserListResponse>("/api/v1/admin/users", {
    query: params,
    headers: opts?.headers,
  });
}

/**
 * Add an account. Site administrators only, and — unlike the public signup
 * form — unaffected by `TF_ALLOW_SIGNUP`: on an instance that closed signup
 * this is the only way to add anyone. 201 on success; 409 when the username
 * is taken, 400 for a reserved or malformed one.
 */
export function createAdminUser(
  req: AdminUserCreateRequest,
  opts?: FetchOpts,
): Promise<ApiResult<AdminUserResponse>> {
  return apiFetch<AdminUserResponse>("/api/v1/admin/users", {
    method: "POST",
    body: req,
    headers: opts?.headers,
  });
}

/**
 * Reset an account's password, flip its site administrator flag, or both.
 * Absent fields are left unchanged; a request that sets neither is a 400.
 */
export function updateAdminUser(
  username: string,
  req: AdminUserUpdateRequest,
  opts?: FetchOpts,
): Promise<ApiResult<AdminUserResponse>> {
  return apiFetch<AdminUserResponse>(`/api/v1/admin/users/${encodeURIComponent(username)}`, {
    method: "PATCH",
    body: req,
    headers: opts?.headers,
  });
}

/**
 * The `error.type` values these endpoints answer with, mapped to translated
 * copy — the same shape as `orgErrorKey` in lib/orgs.ts. Returns null when
 * the caller should fall back to `errorMessage(t, result)`.
 */
const ERROR_KEYS: Record<string, MessageKey> = {
  last_admin: "settings.adminUsers.errors.lastAdmin",
  self_demote: "settings.adminUsers.errors.selfDemote",
  reserved_name: "settings.adminUsers.errors.nameReserved",
  conflict: "settings.adminUsers.errors.usernameTaken",
};

export function adminUserErrorKey(
  result: Extract<ApiResult<unknown>, { ok: false }>,
): MessageKey | null {
  const byType = result.type ? ERROR_KEYS[result.type] : undefined;
  if (byType) return byType;
  if (result.status === 401) return "settings.adminUsers.errors.loginRequired";
  if (result.status === 403) return "settings.adminUsers.errors.permissionDenied";
  if (result.status === 404) return "settings.adminUsers.errors.userNotFound";
  return null;
}
