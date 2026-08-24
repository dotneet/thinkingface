import { type ApiResult, apiFetch } from "@/lib/api";
import type { RepoKind } from "@/types/api";

/**
 * Branch and tag mutations.
 *
 * Unlike the rest of `lib/`, these hit the **HuggingFace-compatible** routes
 * (`/api/{kind}s/{ns}/{name}/branch/...`) rather than `/api/v1/...`: the four
 * handlers already existed for `HfApi.create_branch` and friends, and there is
 * no separate UI endpoint to add. Their asymmetric shape is huggingface_hub's
 * and not ours — creating a tag puts the *revision being tagged* in the path
 * and the tag name in the body, while deleting one puts the tag name in the
 * path.
 *
 * A session cookie authenticates as `write` scope, so these work from the
 * browser exactly as they do for a token client.
 */

/** The `hfRefResult` every one of the four endpoints answers with. */
export type RefMutationResult = {
  name: string;
  ref: string;
  targetCommit: string;
};

/**
 * `encodeURIComponent`, not a path join: a ref name may contain `/`
 * (`feature/my-change`), and the backend's `pathParam` unescapes each segment,
 * so the slash has to arrive as `%2F` or the route does not match.
 */
function refBase(kind: RepoKind, ns: string, name: string): string {
  return `/api/${kind}s/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`;
}

export function createBranch(
  kind: RepoKind,
  ns: string,
  name: string,
  branch: string,
  startingPoint: string,
): Promise<ApiResult<RefMutationResult>> {
  return apiFetch<RefMutationResult>(
    `${refBase(kind, ns, name)}/branch/${encodeURIComponent(branch)}`,
    { method: "POST", body: { startingPoint } },
  );
}

export function deleteBranch(
  kind: RepoKind,
  ns: string,
  name: string,
  branch: string,
): Promise<ApiResult<RefMutationResult>> {
  return apiFetch<RefMutationResult>(
    `${refBase(kind, ns, name)}/branch/${encodeURIComponent(branch)}`,
    { method: "DELETE" },
  );
}

/**
 * `rev` is the revision being tagged and `tag` is the new name — the opposite
 * way round from {@link deleteTag}, which is what huggingface_hub does.
 * An empty `message` produces a lightweight tag; a non-empty one an annotated
 * tag object, the way `git tag -m` does.
 */
export function createTag(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  tag: string,
  message = "",
): Promise<ApiResult<RefMutationResult>> {
  return apiFetch<RefMutationResult>(`${refBase(kind, ns, name)}/tag/${encodeURIComponent(rev)}`, {
    method: "POST",
    body: { tag, message },
  });
}

export function deleteTag(
  kind: RepoKind,
  ns: string,
  name: string,
  tag: string,
): Promise<ApiResult<RefMutationResult>> {
  return apiFetch<RefMutationResult>(`${refBase(kind, ns, name)}/tag/${encodeURIComponent(tag)}`, {
    method: "DELETE",
  });
}
