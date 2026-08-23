import type { ApiResult } from "@/lib/api";
import { authHeaders } from "@/lib/server-auth";
import type { User } from "@/types/api";

function apiBaseUrl(): string {
  return process.env.API_URL ?? process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";
}

/**
 * Server-only: resolves the current logged-in user (if any) by forwarding
 * the incoming request's session cookie to the backend. Safe to call from
 * every page since it never throws — an unreachable backend or missing
 * session both resolve to "not logged in".
 */
export async function getCurrentUser(): Promise<ApiResult<{ user: User }>> {
  try {
    const headers = await authHeaders();
    const res = await fetch(`${apiBaseUrl().replace(/\/$/, "")}/api/v1/me`, {
      headers,
      cache: "no-store",
    });
    if (!res.ok) {
      return { ok: false, status: res.status, message: res.statusText };
    }
    const data = (await res.json()) as { user: User };
    return { ok: true, data };
  } catch (err) {
    return { ok: false, status: 0, message: err instanceof Error ? err.message : "network error" };
  }
}
