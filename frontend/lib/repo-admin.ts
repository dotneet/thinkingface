import { type ApiResult, apiFetch } from "@/lib/api";
import type { FetchOpts } from "@/lib/repos";
import type { RepoDetail, RepoKind } from "@/types/api";

/**
 * The owner-level operations on a repository as a whole: freezing it
 * read-only, thawing it again, and destroying it. They live apart from
 * `lib/repos.ts` (which is the read surface plus create) because every one of
 * them needs the caller to hold admin over the namespace, and because delete
 * is irreversible — the UI must never reach for one of these by accident.
 */

function repoPath(kind: RepoKind, ns: string, name: string): string {
  return `/api/v1/repos/${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`;
}

/** Makes the repository read-only. Reads, clones and downloads keep working. */
export function archiveRepo(
  kind: RepoKind,
  ns: string,
  name: string,
  opts?: FetchOpts,
): Promise<ApiResult<{ repo: RepoDetail }>> {
  return apiFetch<{ repo: RepoDetail }>(`${repoPath(kind, ns, name)}/archive`, {
    method: "POST",
    headers: opts?.headers,
  });
}

/** Lifts the archive, restoring write access. */
export function unarchiveRepo(
  kind: RepoKind,
  ns: string,
  name: string,
  opts?: FetchOpts,
): Promise<ApiResult<{ repo: RepoDetail }>> {
  return apiFetch<{ repo: RepoDetail }>(`${repoPath(kind, ns, name)}/archive`, {
    method: "DELETE",
    headers: opts?.headers,
  });
}

/**
 * Deletes the repository, its git history and its exported objects. There is
 * no undo; the caller is expected to have taken a typed confirmation first
 * (see RepoDangerZone).
 */
export function deleteRepo(
  kind: RepoKind,
  ns: string,
  name: string,
  opts?: FetchOpts,
): Promise<ApiResult<void>> {
  return apiFetch<void>(repoPath(kind, ns, name), {
    method: "DELETE",
    headers: opts?.headers,
  });
}
