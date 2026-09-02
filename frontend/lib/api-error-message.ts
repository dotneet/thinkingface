import type { ApiResult } from "@/lib/api";
import type { MessageKey, Translator } from "@/lib/i18n";

export type FailedApiResult = Extract<ApiResult<unknown>, { ok: false }>;

/**
 * Maps a backend `error.type` (apitypes.ApiError.Type) to the dictionary key
 * that translates it. Keep in sync with every `writeError` call site under
 * backend/internal/api (errors.go, redirect.go, transfers.go, git.go) — see
 * [S12] in todo/security-audit-findings.md.
 *
 * **Every key here must be a complete sentence with no `{placeholder}` in it.**
 * These are rendered by `t(key)` with no params, and `createTranslator`
 * returns the template untouched when it is given none — so a key that takes
 * a placeholder prints the literal `{detail}` on the user's screen. The
 * interpolated variants live in {@link DETAIL_KEYS}, which is only ever
 * consulted when there is a detail to put in them. Asserted by
 * lib/api-error-message.test.ts.
 */
const ERROR_TYPE_KEYS: Record<string, MessageKey> = {
  bad_request: "errors.badRequestGeneric",
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
  account_disabled: "errors.accountDisabled",
  // Two states, two sentences: approval_pending answers the sign-up that
  // just created the account, account_pending answers a later sign-in by
  // one still waiting. Telling somebody "your account is being reviewed"
  // when they have not registered yet reads as a bug.
  approval_pending: "errors.approvalPending",
  account_pending: "errors.accountPending",
  rate_limited: "errors.rateLimited",
  overloaded: "errors.overloaded",
  range_not_satisfiable: "errors.rangeNotSatisfiable",
  // 503 from net/http's TimeoutHandler rather than from writeError
  // (handlerTimeoutBody in backend/internal/api/server.go), but it is spelled
  // in the same error shape, so it arrives here like any other type.
  timeout: "errors.timeout",
};

/**
 * Error types whose `message` is a *reason written for the person reading it*,
 * and is therefore interpolated into the translated sentence instead of being
 * thrown away. Both are 4xx "you asked for something you can't have, here is
 * which thing" answers, assembled from names the caller already supplied:
 *
 * - `bad_request` — "name must not contain spaces".
 * - `forbidden` — "sign-up is disabled on this instance"
 *   (backend/internal/api/auth.go), "you must have admin access to acme to
 *   delete acme/bert", "this token is read-only". Replacing all of those with
 *   the flat "You don't have permission to do this." is what made a sign-up on
 *   a closed instance read as a permissions bug.
 * - `conflict` — "main is the default branch of acme/bert and cannot be
 *   deleted" (refs.go), "docs/a.md already exists; delete it first or pick
 *   another path" (edit.go), "main is a tag, not a branch; uploads must target
 *   a branch" (commit.go). The generic "That already exists." was not merely
 *   vague for these, it was wrong.
 *
 *   All fourteen `conflict(w, …)` call sites under backend/internal/api were
 *   read before adding it: every one is a fixed English phrase, optionally
 *   joined to a name the caller itself supplied (`newPath`, `rev`, `req.Name`,
 *   `branch`, `repo.FullName()`) or to an operation word from a closed set
 *   ("branch"/"tag", "uploads"/"edits"/"renames"/"deletions"/"commits"). The
 *   underlying `error` value never reaches the client on this path —
 *   `handleStoreError` logs it and sends only the operation name, the same
 *   name `internalError` already puts on the wire — so there is no driver
 *   text, path or stack to leak. The one rough edge is `handleStoreError`'s
 *   own `op + ": already exists"`, which reads like a log line ("update
 *   organisation: already exists"); that is a wording problem to fix in the
 *   backend, and it is still more use than "That already exists."
 *
 * Everything else keeps its generic translation. The line is drawn at *who the
 * message was written for*, not at how useful it looks: `internal_error` and
 * `overloaded` are server-side conditions whose text is written for an
 * operator reading logs, and are exactly where an implementation detail (a
 * driver error, a path, a host name) would leak onto a stranger's screen if
 * this list grew carelessly. `unauthorized` and `not_found` stay generic too —
 * their backend text says nothing the translated copy doesn't, and `not_found`
 * echoes a resource name that the generic copy deliberately doesn't confirm.
 *
 * Adding a type here means auditing every `writeError` call site that emits it
 * under backend/internal/api first.
 */
const DETAIL_KEYS: Record<string, MessageKey> = {
  bad_request: "errors.badRequest",
  forbidden: "errors.forbiddenDetail",
  conflict: "errors.conflictDetail",
};

/**
 * Turns an `apiFetch` failure into a message in the current locale instead
 * of the backend-authored English string in `result.message` ([S12]).
 *
 * - `status === 0` (backend unreachable / network failure) always gets its
 *   own copy, since `result.message` there is a raw `fetch` error, not
 *   anything the backend wrote.
 * - A recognized `type` is translated. The types in {@link DETAIL_KEYS} keep
 *   the backend's reason, interpolated into the translated sentence; an empty
 *   (or whitespace-only) `message` falls back to the placeholder-free wording
 *   in {@link ERROR_TYPE_KEYS} rather than rendering a dangling "…: " — or,
 *   worse, the raw "{detail}" the template would print if it were rendered
 *   with no params.
 * - An unrecognized (or missing) `type` falls back to `result.message`
 *   verbatim. This only happens for a `type` this dictionary doesn't know
 *   about yet — every type the backend currently sends is mapped above —
 *   so callers should treat it as a rare escape hatch, not the normal path.
 */
export function errorMessage(t: Translator, result: FailedApiResult): string {
  if (result.status === 0) return t("errors.networkError");
  const key = result.type ? ERROR_TYPE_KEYS[result.type] : undefined;
  if (!key) return result.message;
  const detail = result.message.trim();
  const detailKey = result.type ? DETAIL_KEYS[result.type] : undefined;
  if (detailKey && detail) return t(detailKey, { detail });
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
