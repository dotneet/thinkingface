import { permanentRedirect } from "next/navigation";
import { type ApiResult, isRepoMoved } from "@/lib/api";
import { repoBase } from "@/lib/paths";
import type { RepoKind } from "@/types/api";

/**
 * When `result` failed because the repository has been transferred or
 * renamed (a 404 carrying `movedTo`, see docs/repo-transfer-design.md §9),
 * permanently redirects to the same route rebuilt at the repository's
 * current location and never returns.
 *
 * `toHref` is either a `RepoKind` — for the plain `/{kind}s/{ns}/{name}`
 * route — or a function that receives the new `(namespace, name)` and
 * returns the full path (with query string) the page should have used had
 * the repository always lived there. Pass the function form and build it by
 * calling the same `repoXxxHref` helper the page already uses for its own
 * links (e.g. `repoTreeHref`), so the rest of the path and any query
 * parameters carry over automatically instead of being reconstructed here.
 *
 * Call this ahead of `isNotFound()` / `notFound()`: a repo_moved response is
 * a 404, and the redirect must take priority over the generic not-found page.
 */
export function redirectIfRepoMoved(
  result: ApiResult<unknown>,
  toHref: RepoKind | ((namespace: string, name: string) => string),
): void {
  if (!isRepoMoved(result)) return;
  const build =
    typeof toHref === "function"
      ? toHref
      : (namespace: string, name: string) => repoBase(toHref, namespace, name);
  permanentRedirect(build(result.movedTo.namespace, result.movedTo.name));
}
