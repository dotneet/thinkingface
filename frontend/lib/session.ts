import type { ApiResult } from "@/lib/api";
import { authHeaders } from "@/lib/server-auth";
import type { ApiErrorBody, User } from "@/types/api";

function apiBaseUrl(): string {
  return process.env.API_URL ?? process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";
}

/**
 * Server-only: resolves the current logged-in user (if any) by forwarding
 * the incoming request's session cookie to the backend. Safe to call from
 * every page since it never throws — an unreachable backend or missing
 * session both resolve to "not logged in".
 *
 * Failures follow the `apiFetch` shape: the backend's `error.type` is carried
 * so `errorMessage` can translate it, and the `message` is never a raw
 * exception string — a network failure reports a fixed message (rendered as
 * `errors.networkError`), an HTTP failure the parsed body or the status line.
 */
export async function getCurrentUser(): Promise<ApiResult<{ user: User }>> {
  try {
    const headers = await authHeaders();
    const res = await fetch(`${apiBaseUrl().replace(/\/$/, "")}/api/v1/me`, {
      headers,
      cache: "no-store",
    });
    if (!res.ok) {
      let body: ApiErrorBody | undefined;
      try {
        body = (await res.json()) as ApiErrorBody;
      } catch {
        body = undefined;
      }
      return {
        ok: false,
        status: res.status,
        message: body?.error?.message ?? `${res.status} ${res.statusText}`,
        type: body?.error?.type,
      };
    }
    const data = (await res.json()) as { user: User };
    return { ok: true, data };
  } catch {
    return { ok: false, status: 0, message: "Network error" };
  }
}
