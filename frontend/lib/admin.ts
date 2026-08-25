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
import type { FailedApiResult } from "@/lib/api-error-message";
import type { MessageKey } from "@/lib/i18n";
import type { FetchOpts } from "@/lib/repos";
import type {
  AdminNamespaceListResponse,
  AdminNamespaceQuotaRequest,
  AdminNamespaceUsage,
  AdminUserCreateRequest,
  AdminUserListResponse,
  AdminUserResponse,
  AdminUserUpdateRequest,
  PasswordChangeRequest,
  SyncJobListResponse,
  UserApproval,
} from "@/types/api";

export type { AdminNamespaceUsage, SyncJob } from "@/types/api";

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
 * Suspend or restore an account — `updateAdminUser` with the one field, named
 * so a call site reads as what it does.
 *
 * Suspending stops every identity path at once (session, password, HTTP
 * Basic, access token, SSH key) and revokes the account's sessions
 * immediately. It destroys nothing: restoring brings the account back exactly
 * as it was, minus anything `revokeAdminUserCredentials` deleted in between,
 * and minus the sessions — those stay dead on purpose.
 *
 * 400 `self_disable` for your own account and 409 `last_admin` for the last
 * remaining site administrator, both refused before anything is written.
 */
export function setAdminUserDisabled(
  username: string,
  disabled: boolean,
  opts?: FetchOpts,
): Promise<ApiResult<AdminUserResponse>> {
  return updateAdminUser(username, { disabled }, opts);
}

/**
 * Admit an account from the sign-up waiting room, or put one back into it —
 * `updateAdminUser` with the one field, named so a call site reads as what it
 * does.
 *
 * An account is pending when it self-registered on an instance running
 * `TF_SIGNUP_REQUIRE_APPROVAL`. Until it is approved it authenticates on
 * nothing at all — not its password, not an access token, not an SSH key —
 * so approving is what actually lets somebody in. Putting one back revokes
 * its sessions in the same statement, the way suspending does.
 *
 * It is independent of `disabled`: approving does not un-suspend and
 * restoring does not approve. 400 `self_pending` for your own account and
 * 409 `last_admin` for the last remaining site administrator.
 */
export function setAdminUserApproval(
  username: string,
  approval: UserApproval,
  opts?: FetchOpts,
): Promise<ApiResult<AdminUserResponse>> {
  return updateAdminUser(username, { approval }, opts);
}

/**
 * Delete every access token and registered SSH key the account holds, and
 * revoke its sessions. 204 on success.
 *
 * Irreversible, and deliberately not part of the PATCH above: suspension is a
 * switch that can be flipped back, this is a deletion. It does not suspend
 * the account either — it is for credentials that are suspected (a lost
 * laptop, a token in a build log) on an account that should keep working once
 * new ones are issued. 400 `self_revoke` on your own account, whose tokens
 * and keys are managed from /settings instead.
 */
export function revokeAdminUserCredentials(
  username: string,
  opts?: FetchOpts,
): Promise<ApiResult<void>> {
  return apiFetch<void>(`/api/v1/admin/users/${encodeURIComponent(username)}/revoke-credentials`, {
    method: "POST",
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
  self_disable: "settings.adminUsers.errors.selfDisable",
  self_pending: "settings.adminUsers.errors.selfPending",
  self_revoke: "settings.adminUsers.errors.selfRevoke",
  // Every /api/v1/admin endpoint accepts the session cookie only. A browser
  // on these screens always has one, so this is what a session that expired
  // mid-visit looks like — the message says to sign in again rather than
  // repeating the generic "no permission".
  session_required: "settings.adminUsers.errors.sessionRequired",
  reserved_name: "settings.adminUsers.errors.nameReserved",
  conflict: "settings.adminUsers.errors.usernameTaken",
};

/**
 * The statuses every /api/v1/admin endpoint can answer with, mapped once.
 *
 * 401 and 403 mean the same thing whichever admin screen asked — you are
 * signed out, or you are not a site administrator — so only the 404 differs,
 * and that is what each caller names. The three `…ErrorKey` functions below
 * used to spell all three lines out and had already drifted in their argument
 * types.
 */
function adminStatusKey(result: FailedApiResult, notFound: MessageKey): MessageKey | null {
  if (result.status === 401) return "settings.adminUsers.errors.loginRequired";
  if (result.status === 403) return "settings.adminUsers.errors.permissionDenied";
  if (result.status === 404) return notFound;
  return null;
}

export function adminUserErrorKey(result: FailedApiResult): MessageKey | null {
  const byType = result.type ? ERROR_KEYS[result.type] : undefined;
  if (byType) return byType;
  return adminStatusKey(result, "settings.adminUsers.errors.userNotFound");
}

export type SyncJobsParams = {
  limit?: number;
  offset?: number;
};

/**
 * The jobs that exhausted their attempts and parked. Site administrators
 * only, from a browser session: like every /api/v1/admin endpoint this
 * refuses access tokens and HTTP Basic outright (403 `session_required`).
 *
 * Only failed jobs are listed — a job still retrying is not an operator's
 * problem yet — so an empty page is the healthy state, not a missing feature.
 */
export function listFailedSyncJobs(
  params: SyncJobsParams = {},
  opts?: FetchOpts,
): Promise<ApiResult<SyncJobListResponse>> {
  return apiFetch<SyncJobListResponse>("/api/v1/admin/sync-jobs", {
    query: params,
    headers: opts?.headers,
  });
}

/**
 * Put one parked job back in the queue with a fresh attempt budget. 204 on
 * success; 404 when the job is no longer failed — already retried by someone
 * else, or gone with its repository — which is why the caller should reload
 * the listing on that status rather than treat it as a hard error.
 *
 * The worker picks the job up on its next poll (about ten seconds), so a 204
 * means "requeued", not "succeeded".
 */
export function retrySyncJob(id: number, opts?: FetchOpts): Promise<ApiResult<void>> {
  return apiFetch<void>(`/api/v1/admin/sync-jobs/${id}/retry`, {
    method: "POST",
    headers: opts?.headers,
  });
}

/**
 * Same idea as `adminUserErrorKey`, for the sync-job endpoints: they answer
 * no types of their own, so this is purely the status mapping. Returns null
 * when the caller should fall back to `errorMessage(t, result)`.
 */
export function syncJobErrorKey(result: FailedApiResult): MessageKey | null {
  return adminStatusKey(result, "settings.adminSyncJobs.errors.jobGone");
}

export type AdminNamespacesParams = {
  search?: string;
  limit?: number;
  offset?: number;
};

/**
 * Every namespace on the instance with what it stores and what it may store.
 * Site administrators only, from a browser session, like the rest of
 * /api/v1/admin.
 *
 * `quota_bytes` is the namespace's own override and `effective_quota_bytes`
 * what is actually enforced — the override when there is one, otherwise the
 * instance default (`default_quota_bytes`). Null means unlimited in both.
 */
export function listAdminNamespaces(
  params: AdminNamespacesParams = {},
  opts?: FetchOpts,
): Promise<ApiResult<AdminNamespaceListResponse>> {
  return apiFetch<AdminNamespaceListResponse>("/api/v1/admin/namespaces", {
    query: params,
    headers: opts?.headers,
  });
}

/**
 * Set or clear one namespace's storage quota.
 *
 * `quotaBytes` is required and nullable, and the two "no number" cases are
 * different instructions: `null` removes the override so the instance default
 * applies again, while `0` is a real quota of zero bytes — a namespace that
 * may hold repositories but upload nothing. Never send one meaning the other.
 *
 * The quota is enforced on the LFS upload path, so lowering it below what a
 * namespace already stores refuses the next upload rather than deleting
 * anything: 200 here always means "recorded", never "reclaimed".
 */
export function setNamespaceQuota(
  namespace: string,
  quotaBytes: number | null,
  opts?: FetchOpts,
): Promise<ApiResult<AdminNamespaceUsage>> {
  const body: AdminNamespaceQuotaRequest = { quota_bytes: quotaBytes };
  return apiFetch<AdminNamespaceUsage>(
    `/api/v1/admin/namespaces/${encodeURIComponent(namespace)}`,
    {
      method: "PATCH",
      body,
      headers: opts?.headers,
    },
  );
}

/**
 * Same idea as `adminUserErrorKey`, for the namespace quota endpoints. They
 * answer no types of their own, so this is purely the status mapping; returns
 * null when the caller should fall back to `errorMessage(t, result)`.
 */
export function namespaceQuotaErrorKey(result: FailedApiResult): MessageKey | null {
  if (result.type === "session_required") return "settings.adminUsers.errors.sessionRequired";
  return adminStatusKey(result, "settings.adminQuotas.errors.namespaceGone");
}
