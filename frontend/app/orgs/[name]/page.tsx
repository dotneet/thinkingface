import { permanentRedirect } from "next/navigation";
import { namespaceHref } from "@/lib/namespace";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

type Search = { tab?: string };

/**
 * `/orgs/{name}` is now only a redirect to the one namespace page, `/{name}`
 * (docs/namespace-design.md §4.1). It stays so bookmarks and external links
 * made before the merge keep working; the organisation's settings screens
 * remain under `/orgs/{name}/settings`.
 */
export default async function OrgPage({
  params,
  searchParams,
}: {
  params: Promise<{ name: string }>;
  searchParams: Promise<Search>;
}) {
  const [rawParams, sp] = await Promise.all([params, searchParams]);
  const { name } = decodeRouteParams(rawParams);
  // The old page spelled the datasets tab `?tab=dataset` (a RepoKind); the
  // namespace page names tabs after the listing, so translate the one value
  // that differs and drop everything else, which belonged to the old
  // tab's result set.
  const tab = sp.tab === "dataset" ? "datasets" : sp.tab;
  const suffix = tab === "datasets" ? "?tab=datasets" : "";
  permanentRedirect(`${namespaceHref(name)}${suffix}`);
}
