import type { ApiResult } from "@/lib/api";
import { isDetailErrorType } from "@/lib/api-error-message";
import { typeForStatus } from "@/lib/error-status";
import { publicApiBase } from "@/lib/paths";
import type { ApiErrorBody, RepoKind, UploadFilesResponse } from "@/types/api";

/** One file plus the repository path it should land at. */
export type UploadItem = { path: string; file: File };

export type UploadOptions = {
  message?: string;
  description?: string;
  /** Called with the bytes sent so far. `total` is 0 until the browser knows it. */
  onProgress?: (loaded: number, total: number) => void;
  signal?: AbortSignal;
};

/**
 * Upload files to a branch as a single commit.
 *
 * XMLHttpRequest rather than `apiFetch`, for one reason: `fetch` reports no
 * upload progress. This repository's files run to gigabytes, and a dialog
 * that sits there saying nothing for ten minutes reads as broken. Everything
 * else follows `apiFetch`'s contract — it never throws, and every failure
 * (HTTP, network, abort) comes back as `{ ok: false }` so the caller always
 * has an error state to render.
 *
 * The parts are appended path-then-file, in order: the backend reads the body
 * as a stream, so a `path` field binds to the file part that follows it.
 */
export function uploadFiles(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  items: UploadItem[],
  options: UploadOptions = {},
): Promise<ApiResult<UploadFilesResponse>> {
  const url =
    `${publicApiBase().replace(/\/$/, "")}/api/v1/upload/` +
    `${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/${encodeURIComponent(rev)}`;

  const form = new FormData();
  if (options.message) form.append("message", options.message);
  if (options.description) form.append("description", options.description);
  for (const item of items) {
    form.append("path", item.path);
    form.append("file", item.file, item.file.name);
  }

  return new Promise((resolve) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", url);
    // The session cookie lives on the API origin; this is the XHR spelling of
    // `credentials: "include"`.
    xhr.withCredentials = true;

    xhr.upload.onprogress = (event) => {
      options.onProgress?.(event.loaded, event.lengthComputable ? event.total : 0);
    };
    // Both carry a `type` so `errorMessage` translates them: a dead
    // connection is `network_error` (→ errors.networkError), a deliberate
    // abort is `upload_cancelled` (→ errors.uploadCancelled) and must not
    // read as a broken connection.
    xhr.onerror = () =>
      resolve({ ok: false, status: 0, message: "Network error", type: "network_error" });
    xhr.onabort = () =>
      resolve({ ok: false, status: 0, message: "Upload cancelled", type: "upload_cancelled" });
    xhr.onload = () => resolve(parseUploadResponse(xhr));
    options.signal?.addEventListener("abort", () => xhr.abort(), { once: true });

    xhr.send(form);
  });
}

/**
 * Mirrors maxUploadFileBytes / gitrepo.LFSInlineThreshold /
 * maxUploadInlineTotalBytes in backend/internal/api/upload.go (and
 * backend/internal/gitrepo/gitattributes.go for the threshold), so the dialog
 * can reject an upload that is certain to fail before a single byte of it is
 * sent, rather than after every byte has been streamed to the server.
 *
 * `MAX_UPLOAD_FILE_BYTES` is unconditional -- it bounds every file part
 * regardless of how it is routed. The other two only bound files that are
 * *not* routed to LFS, and that routing depends on the repository's own
 * .gitattributes (a pattern can force or negate LFS for a path), which this
 * module has no way to read. `LFS_INLINE_THRESHOLD_BYTES` is only a stand-in
 * for "no matching pattern, size decides" -- the common case -- so
 * evaluateUploadSizes is a best-effort early check, not a guarantee: the
 * server enforces the real limits regardless of what this returns.
 */
export const MAX_UPLOAD_FILE_BYTES = 10 * 1024 * 1024 * 1024; // 10 GiB
export const LFS_INLINE_THRESHOLD_BYTES = 10 * 1024 * 1024; // 10 MiB
export const MAX_UPLOAD_INLINE_TOTAL_BYTES = 128 * 1024 * 1024; // 128 MiB

export type UploadSizeIssue =
  | { type: "fileTooLarge"; fileName: string; limit: number }
  | { type: "inlineTotalTooLarge"; limit: number };

/**
 * Checks a picked file list against the size limits above, before any of it
 * is sent. Returns the first problem found, or `null` when nothing here would
 * make the server refuse the request on size alone.
 *
 * Only two of the three backend limits are checked (see the constants'
 * doc comment): `maxUploadInlineBytes` (32 MiB, the per-file inline cap) is
 * not, because it can never fire under the size-based heuristic this function
 * uses -- LFS_INLINE_THRESHOLD_BYTES (10 MiB) is below it, so any file this
 * function treats as "likely inline" is already under 32 MiB by definition.
 */
export function evaluateUploadSizes(
  files: { name: string; size: number }[],
): UploadSizeIssue | null {
  for (const file of files) {
    if (file.size > MAX_UPLOAD_FILE_BYTES) {
      return { type: "fileTooLarge", fileName: file.name, limit: MAX_UPLOAD_FILE_BYTES };
    }
  }
  const likelyInlineTotal = files
    .filter((file) => file.size < LFS_INLINE_THRESHOLD_BYTES)
    .reduce((sum, file) => sum + file.size, 0);
  if (likelyInlineTotal > MAX_UPLOAD_INLINE_TOTAL_BYTES) {
    return { type: "inlineTotalTooLarge", limit: MAX_UPLOAD_INLINE_TOTAL_BYTES };
  }
  return null;
}

/** Maps a finished XHR onto the same discriminated union `apiFetch` returns. */
function parseUploadResponse(xhr: XMLHttpRequest): ApiResult<UploadFilesResponse> {
  let parsed: unknown;
  try {
    parsed = xhr.responseText ? JSON.parse(xhr.responseText) : undefined;
  } catch {
    parsed = undefined;
  }
  if (xhr.status < 200 || xhr.status >= 300) {
    const body = parsed as ApiErrorBody | undefined;
    if (body?.error?.type !== undefined) {
      // A backend error body carries its own `type`, authored for the person
      // reading it — `errorMessage` may interpolate its message where the
      // type allows (see DETAIL_KEYS in lib/api-error-message.ts).
      return {
        ok: false,
        status: xhr.status,
        message: body.error.message ?? `${xhr.status} ${xhr.statusText}`,
        type: body.error.type,
      };
    }
    // A proxy/gateway failure usually carries no body, so derive a type from
    // the status — otherwise `errorMessage` would have to print the raw
    // status line (`${status} ${statusText}`) on screen. A detail-capable
    // type is never synthesized here: its translation interpolates the
    // message, and the only message this branch has is that same bare status
    // line (`400 Bad Request`), which must stay off the screen (see
    // `typeForStatus` in lib/error-status.ts). Leaving `type` undefined lets
    // `errorMessage` fall back to the generic sentence for the status instead.
    const type = typeForStatus(xhr.status);
    const safeType = type !== undefined && !isDetailErrorType(type) ? type : undefined;
    return {
      ok: false,
      status: xhr.status,
      message: `${xhr.status} ${xhr.statusText}`,
      ...(safeType !== undefined ? { type: safeType } : {}),
    };
  }
  return { ok: true, data: parsed as UploadFilesResponse };
}
