import { type ApiResult, apiFetch } from "@/lib/api";
import type { FetchOpts } from "@/lib/repos";
import type { User } from "@/types/api";

/**
 * The signed-in account. `opts` exists for the Server Components that need
 * it (the /settings layout decides whether to show the site-admin nav item):
 * `credentials: "include"` does nothing on the server, so the `tf_session`
 * cookie has to be forwarded through `authHeaders()` — CLAUDE.md invariant 2.
 */
export function getMe(opts?: FetchOpts): Promise<ApiResult<{ user: User }>> {
  return apiFetch<{ user: User }>("/api/v1/me", { headers: opts?.headers });
}

export function login(username: string, password: string): Promise<ApiResult<{ user: User }>> {
  return apiFetch<{ user: User }>("/api/v1/auth/login", {
    method: "POST",
    body: { username, password },
  });
}

export function signup(
  username: string,
  email: string,
  password: string,
): Promise<ApiResult<{ user: User }>> {
  return apiFetch<{ user: User }>("/api/v1/auth/signup", {
    method: "POST",
    body: { username, email, password },
  });
}

export function logout(): Promise<ApiResult<void>> {
  return apiFetch<void>("/api/v1/auth/logout", { method: "POST" });
}
