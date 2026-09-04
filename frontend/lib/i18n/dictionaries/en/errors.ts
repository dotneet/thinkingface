// errors: translated copy for backend `error.type` values (apitypes.ApiError.Type
// — docs/dev/api-contract.md's error contract). `lib/api-error-message.ts` maps an
// `ApiResult` failure's `type` to one of these keys instead of showing the
// backend's raw English `message` on screen (see [S12] in
// todo/security-audit-findings.md). Keep this in sync with every `writeError`
// call site under backend/internal/api (errors.go, redirect.go,
// transfers.go, git.go).
// NOTE: the en dictionary is the source of truth for shape, so it must not be `as const`.
export const errors = {
  networkError: "Couldn't reach the server. Check your connection and try again.",
  // A deliberate abort of an in-flight upload (lib/upload.ts tags it
  // `upload_cancelled`): the connection is fine, the user asked to stop, so
  // this must not read as a broken network.
  uploadCancelled: "The upload was cancelled.",
  // bad_request messages carry a specific, actionable detail from the
  // backend ("name must not contain spaces"), so it's appended rather than
  // discarded like the other types' raw text.
  badRequest: "Invalid request: {detail}",
  // …and what to say when it carries no detail at all. `badRequest` is a
  // template, so rendering it without params would print the literal
  // "{detail}" on screen; ERROR_TYPE_KEYS maps bad_request here and only
  // DETAIL_KEYS reaches for the interpolated sentence above.
  badRequestGeneric: "The request was invalid.",
  unauthorized: "You need to be logged in to do this.",
  forbidden: "You don't have permission to do this.",
  // forbidden messages name the thing that was refused ("sign-up is disabled
  // on this instance", "you must have admin access to acme/bert"), so the
  // reason is interpolated rather than dropped — see DETAIL_KEYS in
  // lib/api-error-message.ts for why only some types get this treatment.
  forbiddenDetail: "Not allowed: {detail}",
  notFound: "Not found.",
  conflict: "That already exists.",
  // 409s name the thing they collided with ("main is the default branch of
  // acme/bert and cannot be deleted"), which the flat sentence above does not
  // merely blur but contradicts. Every conflict() call site in
  // backend/internal/api was audited before trusting this text — see
  // DETAIL_KEYS in lib/api-error-message.ts.
  conflictDetail: "Conflict: {detail}",
  payloadTooLarge: "The request is too large.",
  unsupportedMediaType: "The server could not read the format this request was sent in.",
  insufficientStorage: "This namespace has no room left for this upload.",
  insufficientStorageDetail: "Not enough storage: {detail}",
  internalError: "Something went wrong on the server. Try again in a moment.",
  repositoryArchived:
    "This repository is archived and read-only. Unarchive it in the repository settings to make changes.",
  repoMoved: "This repository has moved.",
  transferNotPending: "This transfer is no longer pending.",
  methodNotAllowed: "This action isn't supported here.",
  xetNotSupported: "This operation isn't supported for Xet-backed files.",
  accountDisabled: "This account has been disabled. Contact a site administrator.",
  approvalPending:
    "Your account was created and is waiting for a site administrator to approve it. You will be able to sign in once it is approved.",
  accountPending: "This account is still waiting for a site administrator to approve it.",
  rateLimited: "Too many requests. Try again in a moment.",
  // 503 from the rate limiter's overload guard, not from a per-client quota.
  overloaded: "The server is busy right now. Try again in a moment.",
  // 416 from a ranged file read (backend/internal/api/resolve.go): the range
  // asked for starts at or past the end of the file, which for the UI means
  // the copy it is paging through is not the one on the server any more.
  rangeNotSatisfiable: "This file changed while it was being read. Reload the page and try again.",
  // 504 written by net/http's TimeoutHandler, not by writeError
  // (handlerTimeoutBody in backend/internal/api/server.go).
  timeout: "The server took too long to answer. Try again in a moment.",
};
