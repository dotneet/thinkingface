/**
 * Best-effort backend `error.type` for a failure whose body carried none — a
 * gateway/proxy status line rather than an ApiErrorBody — using the same
 * vocabulary `lib/api-error-message.ts` translates. Statuses with no useful
 * mapping stay `undefined` and degrade to the shared generic failure there.
 *
 * One table, two consumers (review指摘2: no duplicate implementation):
 *
 * - `lib/upload.ts` tags XHR failures that never carried a backend body, so
 *   `errorMessage` translates them instead of printing raw XHR text. It never
 *   bakes a detail-capable type (see `isDetailErrorType` in
 *   lib/api-error-message.ts): those translations interpolate the backend
 *   message, and the only message a bodyless failure has is the bare status
 *   line (`400 Bad Request`), which must never reach the screen (指摘6).
 * - `errorMessage` itself maps an unknown or missing `type` through this and
 *   `ERROR_TYPE_KEYS`, so a proxy/gateway failure degrades to the sentence for
 *   its status (404 → `errors.notFound`) instead of `errors.internalError`.
 *   That path always renders the generic sentence, never the interpolated one:
 *   the message alongside a synthesized type was never audited for
 *   screen-worthiness the way DETAIL_KEYS entries were.
 */
export function typeForStatus(status: number): string | undefined {
  if (status === 400) return "bad_request";
  if (status === 401) return "unauthorized";
  if (status === 403) return "forbidden";
  if (status === 404) return "not_found";
  if (status === 408 || status === 504) return "timeout";
  if (status === 409) return "conflict";
  if (status === 413) return "payload_too_large";
  if (status === 415) return "unsupported_media_type";
  if (status === 429) return "rate_limited";
  if (status === 503) return "overloaded";
  if (status >= 500) return "internal_error";
  return undefined;
}
