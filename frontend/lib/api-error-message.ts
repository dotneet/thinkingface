import type { ApiResult } from "@/lib/api";
import type { MessageKey, Translator } from "@/lib/i18n";

export type FailedApiResult = Extract<ApiResult<unknown>, { ok: false }>;

/**
 * Maps a backend `error.type` (apitypes.ApiError.Type) to the dictionary key
 * that translates it. Keep in sync with every `writeError` call site under
 * backend/internal/api (errors.go, redirect.go, transfers.go, git.go) — see
 * [S12] in todo/security-audit-findings.md.
 */
const ERROR_TYPE_KEYS: Record<string, MessageKey> = {
  bad_request: "errors.badRequest",
  unauthorized: "errors.unauthorized",
  forbidden: "errors.forbidden",
  not_found: "errors.notFound",
  conflict: "errors.conflict",
  payload_too_large: "errors.payloadTooLarge",
  internal_error: "errors.internalError",
  repository_archived: "errors.repositoryArchived",
  repo_moved: "errors.repoMoved",
  transfer_not_pending: "errors.transferNotPending",
  method_not_allowed: "errors.methodNotAllowed",
  xet_not_supported: "errors.xetNotSupported",
  rate_limited: "errors.rateLimited",
  overloaded: "errors.overloaded",
};

/**
 * Turns an `apiFetch` failure into a message in the current locale instead
 * of the backend-authored English string in `result.message` ([S12]).
 *
 * - `status === 0` (backend unreachable / network failure) always gets its
 *   own copy, since `result.message` there is a raw `fetch` error, not
 *   anything the backend wrote.
 * - A recognized `type` is translated. `bad_request` is the one type whose
 *   backend detail is kept — those messages are specific and actionable
 *   ("name must not contain spaces"), unlike the generic prose backing every
 *   other type, so it's interpolated into the translated sentence rather
 *   than replaced by it.
 * - An unrecognized (or missing) `type` falls back to `result.message`
 *   verbatim. This only happens for a `type` this dictionary doesn't know
 *   about yet — every type the backend currently sends is mapped above —
 *   so callers should treat it as a rare escape hatch, not the normal path.
 */
export function errorMessage(t: Translator, result: FailedApiResult): string {
  if (result.status === 0) return t("errors.networkError");
  const key = result.type ? ERROR_TYPE_KEYS[result.type] : undefined;
  if (!key) return result.message;
  if (result.type === "bad_request") return t("errors.badRequest", { detail: result.message });
  return t(key);
}

/**
 * Wraps a failed `ApiResult` so it can be thrown from a react-query
 * `queryFn` (see components/model/model-inspector.tsx,
 * components/parquet/parquet-viewer.tsx) without losing `type`/`status` —
 * `throw new Error(result.message)` would otherwise downgrade every
 * failure to raw English by the time the `error` the component sees.
 */
export class ApiResultError extends Error {
  readonly result: FailedApiResult;

  constructor(result: FailedApiResult) {
    super(result.message);
    this.name = "ApiResultError";
    this.result = result;
  }
}

/**
 * Renders a react-query `error` value as a localized message: an
 * `ApiResultError` goes through `errorMessage`, anything else (a thrown
 * non-API exception) falls back to `fallback`.
 */
export function queryErrorMessage(t: Translator, error: unknown, fallback: string): string {
  if (error instanceof ApiResultError) return errorMessage(t, error.result);
  return fallback;
}
