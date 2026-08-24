import type { ApiResult } from "@/lib/api";
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
    xhr.onerror = () => resolve({ ok: false, status: 0, message: "Network error" });
    xhr.onabort = () => resolve({ ok: false, status: 0, message: "Upload cancelled" });
    xhr.onload = () => resolve(parseUploadResponse(xhr));
    options.signal?.addEventListener("abort", () => xhr.abort(), { once: true });

    xhr.send(form);
  });
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
    return {
      ok: false,
      status: xhr.status,
      message: body?.error?.message ?? `${xhr.status} ${xhr.statusText}`,
      type: body?.error?.type,
    };
  }
  return { ok: true, data: parsed as UploadFilesResponse };
}
