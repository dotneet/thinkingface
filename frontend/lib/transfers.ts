import { type ApiResult, apiFetch } from "@/lib/api";
import type { FetchOpts } from "@/lib/repos";
import type {
  MyTransfersResponse,
  RepoKind,
  RepoTransferRequest,
  RepoTransferResponse,
} from "@/types/api";

function transferPath(kind: RepoKind, ns: string, name: string): string {
  return `/api/v1/repos/${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/transfer`;
}

/**
 * Starts a transfer (docs/dev/repo-transfer-design.md §6-7). The response's
 * `repo` is set when the move completed immediately (200); it is absent
 * when the destination needed approval and the transfer is now pending
 * (202) — both are `ok: true` here, callers branch on `data.repo`.
 */
export function transferRepo(
  kind: RepoKind,
  ns: string,
  name: string,
  req: RepoTransferRequest,
  opts?: FetchOpts,
): Promise<ApiResult<RepoTransferResponse>> {
  return apiFetch<RepoTransferResponse>(transferPath(kind, ns, name), {
    method: "POST",
    body: req,
    headers: opts?.headers,
  });
}

/** The pending transfer for this repository, if any (404 when there is none). */
export function getPendingTransfer(
  kind: RepoKind,
  ns: string,
  name: string,
  opts?: FetchOpts,
): Promise<ApiResult<RepoTransferResponse>> {
  return apiFetch<RepoTransferResponse>(transferPath(kind, ns, name), {
    headers: opts?.headers,
  });
}

/** Cancels this repository's pending transfer (source-side, write access). */
export function cancelTransfer(
  kind: RepoKind,
  ns: string,
  name: string,
  opts?: FetchOpts,
): Promise<ApiResult<void>> {
  return apiFetch<void>(transferPath(kind, ns, name), {
    method: "DELETE",
    headers: opts?.headers,
  });
}

/** Pending transfers the signed-in user can act on: incoming and outgoing. */
export function listMyTransfers(opts?: FetchOpts): Promise<ApiResult<MyTransfersResponse>> {
  return apiFetch<MyTransfersResponse>("/api/v1/me/transfers", { headers: opts?.headers });
}

export function acceptTransfer(
  id: number,
  opts?: FetchOpts,
): Promise<ApiResult<RepoTransferResponse>> {
  return apiFetch<RepoTransferResponse>(`/api/v1/transfers/${id}/accept`, {
    method: "POST",
    headers: opts?.headers,
  });
}

export function rejectTransfer(
  id: number,
  opts?: FetchOpts,
): Promise<ApiResult<RepoTransferResponse>> {
  return apiFetch<RepoTransferResponse>(`/api/v1/transfers/${id}/reject`, {
    method: "POST",
    headers: opts?.headers,
  });
}
