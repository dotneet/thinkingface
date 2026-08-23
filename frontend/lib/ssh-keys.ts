import { type ApiResult, apiFetch } from "@/lib/api";
import type { SSHKeyItem } from "@/types/api";

export function listSSHKeys(): Promise<ApiResult<{ items: SSHKeyItem[] }>> {
  return apiFetch<{ items: SSHKeyItem[] }>("/api/v1/me/ssh-keys");
}

export function createSSHKey(title: string, key: string): Promise<ApiResult<SSHKeyItem>> {
  return apiFetch<SSHKeyItem>("/api/v1/me/ssh-keys", {
    method: "POST",
    body: { title, key },
  });
}

export function deleteSSHKey(id: number): Promise<ApiResult<void>> {
  return apiFetch<void>(`/api/v1/me/ssh-keys/${id}`, { method: "DELETE" });
}
