import type { ApiErrorBody, RepoLocation } from "@/types/api";

/**
 * Base URL resolution:
 * - On the server (Server Components, route handlers) we talk to the
 *   backend over the internal network name, e.g. http://api:8080.
 * - In the browser we must use the publicly reachable URL, e.g.
 *   http://localhost:8080.
 */
function apiBaseUrl(): string {
  if (typeof window === "undefined") {
    return process.env.API_URL ?? process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";
  }
  return process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";
}

export type ApiResult<T> =
  | { ok: true; data: T }
  | {
      ok: false;
      status: number;
      message: string;
      /**
       * The backend's `error.type` (apitypes.ApiError.Type — "not_found",
       * "forbidden", "repository_archived", ...). Undefined for a network
       * failure (status 0) or a non-JSON error body. `lib/api-error-message.ts`
       * maps this to a translated message instead of showing `message`
       * (backend-authored English) directly — see [S12] in
       * todo/security-audit-findings.md.
       */
      type?: string;
      /**
       * Set when the failure is the "repo_moved" 404 (see
       * docs/dev/repo-transfer-design.md §9): the requested repository name is a
       * former name of a repository that has since been transferred or
       * renamed. Pages use `isRepoMoved()` to redirect to the new location
       * instead of rendering a generic not-found page.
       */
      movedTo?: RepoLocation;
      /**
       * The `X-Error-Code` response header, when the backend set one. This is
       * the huggingface_hub compatibility signal (backend/internal/api/refs.go
       * `revisionNotFound`) that tells a 404 caused by an unresolvable
       * revision apart from any other 404 — both carry the same JSON
       * `error.type` ("not_found"), since `revisionOrEmpty`'s read handlers
       * (the UI-facing tree/commits endpoints included) answer a bad revision
       * with 404 rather than folding it into an empty listing. Callers use
       * `isRevisionNotFound()` rather than reading this directly.
       */
      code?: string;
    };

export type ApiFetchOptions = {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  // A string[] value sends the key once per array entry (e.g. tags=a&tags=b),
  // for parameters the backend accepts repeated (see lib/repos.ts).
  query?: Record<string, string | number | boolean | string[] | undefined | null>;
  cache?: RequestCache;
  headers?: Record<string, string>;
  /** ndjson body already encoded as a string (for the commit API) */
  rawBody?: string;
  contentType?: string;
};

function buildUrl(path: string, query?: ApiFetchOptions["query"]): string {
  const base = apiBaseUrl().replace(/\/$/, "");
  const url = new URL(base + path);
  if (query) {
    for (const [key, value] of Object.entries(query)) {
      if (value === undefined || value === null || value === "") continue;
      if (Array.isArray(value)) {
        for (const item of value) {
          if (item === "") continue;
          url.searchParams.append(key, item);
        }
        continue;
      }
      url.searchParams.set(key, String(value));
    }
  }
  return url.toString();
}

/**
 * Thin fetch wrapper around the thinkingface API. Never throws for
 * network/HTTP failures — callers get a discriminated result so every page
 * can degrade to an empty/error state instead of crashing (the backend may
 * simply not be running, including at build time).
 */
export async function apiFetch<T>(
  path: string,
  options: ApiFetchOptions = {},
): Promise<ApiResult<T>> {
  const url = buildUrl(path, options.query);
  try {
    // NOTE on auth forwarding: `credentials: "include"` only matters for
    // browser fetches. Server Components need the tf_session cookie
    // forwarded explicitly via `options.headers` (see `lib/server-auth.ts`'s
    // `authHeaders()`), because this module is also imported by Client
    // Components and therefore cannot statically or dynamically import
    // "next/headers" here — Next.js rejects that at build time even behind
    // a `typeof window` runtime guard, since webpack still has to resolve
    // the import for the client bundle. Every server-side caller in
    // lib/repos.ts, lib/parquet.ts, and lib/experiments.ts accepts an
    // `opts.headers` passthrough for exactly this reason — always thread
    // `await authHeaders()` through from Server Components.
    const res = await fetch(url, {
      method: options.method ?? "GET",
      credentials: "include",
      cache: options.cache ?? "no-store",
      headers: {
        ...(options.rawBody
          ? { "Content-Type": options.contentType ?? "application/x-ndjson" }
          : options.body !== undefined
            ? { "Content-Type": "application/json" }
            : {}),
        ...options.headers,
      },
      body:
        options.rawBody ?? (options.body !== undefined ? JSON.stringify(options.body) : undefined),
    });

    if (res.status === 204) {
      return { ok: true, data: undefined as T };
    }

    const text = await res.text();
    let parsed: unknown;
    if (text) {
      try {
        parsed = JSON.parse(text);
      } catch {
        parsed = undefined;
      }
    }

    if (!res.ok) {
      const errBody = parsed as ApiErrorBody | undefined;
      const message = errBody?.error?.message ?? `${res.status} ${res.statusText}`;
      return {
        ok: false,
        status: res.status,
        message,
        type: errBody?.error?.type,
        movedTo: errBody?.error?.moved_to,
        code: res.headers.get("X-Error-Code") ?? undefined,
      };
    }

    return { ok: true, data: parsed as T };
  } catch (err) {
    // Network error: backend unreachable, DNS failure, etc.
    const message = err instanceof Error ? err.message : "Network error";
    return { ok: false, status: 0, message };
  }
}

export function isNotFound(result: ApiResult<unknown>): boolean {
  return !result.ok && result.status === 404;
}

export function isUnauthorized(result: ApiResult<unknown>): boolean {
  return !result.ok && result.status === 401;
}

/**
 * True when `result` failed because the repository has been transferred or
 * renamed (docs/dev/repo-transfer-design.md §9): a 404 whose body carried
 * `moved_to`. Narrows `result.movedTo` to non-undefined for callers, notably
 * `redirectIfRepoMoved()` in lib/repo-redirect.ts.
 */
export function isRepoMoved(
  result: ApiResult<unknown>,
): result is { ok: false; status: number; message: string; movedTo: RepoLocation } {
  return !result.ok && result.status === 404 && result.movedTo !== undefined;
}

/**
 * True when `result` failed because the revision itself doesn't resolve to
 * anything (an unknown branch/tag/commit, or `git push --delete` racing a
 * viewer) rather than because the requested path is missing at a revision
 * that *does* resolve. Both are plain 404s with `error.type: "not_found"` —
 * the JSON body alone can't tell them apart — so the distinction rides the
 * `X-Error-Code: RevisionNotFound` header (backend/internal/api/refs.go),
 * surfaced here as `result.code`. Pages route this case to a dedicated
 * "this revision doesn't exist, try the default branch" state instead of the
 * generic not-found page (see RepoTree / RepoBlob / RepoCommits).
 */
export function isRevisionNotFound(result: ApiResult<unknown>): boolean {
  return !result.ok && result.status === 404 && result.code === "RevisionNotFound";
}
