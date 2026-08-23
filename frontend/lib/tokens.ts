import { type ApiResult, apiFetch } from "@/lib/api";
import type { CreateTokenResponse, TokenItem } from "@/types/api";

export function listTokens(): Promise<ApiResult<{ items: TokenItem[] }>> {
  return apiFetch<{ items: TokenItem[] }>("/api/v1/tokens");
}

export function createToken(
  name: string,
  scope: "read" | "write",
): Promise<ApiResult<CreateTokenResponse>> {
  return apiFetch<CreateTokenResponse>("/api/v1/tokens", {
    method: "POST",
    body: { name, scope },
  });
}

export function deleteToken(id: number): Promise<ApiResult<void>> {
  return apiFetch<void>(`/api/v1/tokens/${id}`, { method: "DELETE" });
}
