import { type ApiResult, apiFetch } from "@/lib/api";
import type { CreateTokenResponse, TokenItem } from "@/types/api";

export function listTokens(): Promise<ApiResult<{ items: TokenItem[] }>> {
  return apiFetch<{ items: TokenItem[] }>("/api/v1/tokens");
}

// `repos` restricts the token to those repositories ("datasets/ns/name" /
// "models/ns/name"). Omitted or empty mints an unrestricted token, so the key
// is only sent when there is a list -- an empty array is never used to mean
// "unspecified" (DESIGN.md §9).
export function createToken(
  name: string,
  scope: "read" | "write",
  expiresInDays?: number | null,
  repos?: string[],
): Promise<ApiResult<CreateTokenResponse>> {
  return apiFetch<CreateTokenResponse>("/api/v1/tokens", {
    method: "POST",
    body: {
      name,
      scope,
      expires_in_days: expiresInDays ?? null,
      ...(repos && repos.length > 0 ? { repos } : {}),
    },
  });
}

export function deleteToken(id: number): Promise<ApiResult<void>> {
  return apiFetch<void>(`/api/v1/tokens/${id}`, { method: "DELETE" });
}
