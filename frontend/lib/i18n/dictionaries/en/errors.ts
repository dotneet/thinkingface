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
  // bad_request messages carry a specific, actionable detail from the
  // backend ("name must not contain spaces"), so it's appended rather than
  // discarded like the other types' raw text.
  badRequest: "Invalid request: {detail}",
  unauthorized: "You need to be logged in to do this.",
  forbidden: "You don't have permission to do this.",
  notFound: "Not found.",
  conflict: "That already exists.",
  payloadTooLarge: "The request is too large.",
  internalError: "Something went wrong on the server. Try again in a moment.",
  repositoryArchived:
    "This repository is archived and read-only. Unarchive it in the repository settings to make changes.",
  repoMoved: "This repository has moved.",
  transferNotPending: "This transfer is no longer pending.",
  methodNotAllowed: "This action isn't supported here.",
  xetNotSupported: "This operation isn't supported for Xet-backed files.",
  rateLimited: "Too many requests. Try again in a moment.",
  // 503 from the rate limiter's overload guard, not from a per-client quota.
  overloaded: "The server is busy right now. Try again in a moment.",
};
