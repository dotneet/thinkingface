import { type ApiResult, apiFetch } from "@/lib/api";
import type { User } from "@/types/api";

export function getMe(): Promise<ApiResult<{ user: User }>> {
  return apiFetch<{ user: User }>("/api/v1/me");
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
