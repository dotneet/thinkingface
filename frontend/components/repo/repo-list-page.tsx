import { Boxes, Database } from "lucide-react";
import Link from "next/link";
import { Suspense } from "react";
import { RepoCard } from "@/components/repo/repo-card";
import { RepoFacetSidebar } from "@/components/repo/repo-facet-sidebar";
import { RepoListActiveFilters } from "@/components/repo/repo-list-active-filters";
import { RepoListFilters } from "@/components/repo/repo-list-filters";
import { buttonClass } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Pagination } from "@/components/ui/pagination";
import { errorMessage } from "@/lib/api-error-message";
import type { MessageKey } from "@/lib/i18n";
import { getT } from "@/lib/i18n/server";
import {
  listFlagOn,
  listRepos,
  listSearchTags,
  listTriState,
  type RepoListSearch,
  repoListHref,
} from "@/lib/repos";
import { authHeaders } from "@/lib/server-auth";
import type { RepoFacets, RepoKind } from "@/types/api";

const LIMIT = 30;
const EMPTY_FACETS: RepoFacets = { tags: [], licenses: [], tasks: [], relations: [] };

type Sort = "updated" | "created" | "downloads" | "name";

const COPY: Record<
  RepoKind,
  {
    titleKey: MessageKey;
    blurbKey: MessageKey;
    notFoundKey: MessageKey;
    notPublishedKey: MessageKey;
    icon: typeof Database;
    basePath: string;
  }
> = {
  dataset: {
    titleKey: "repoList.datasets.title",
    blurbKey: "repoList.datasets.blurb",
    notFoundKey: "repoList.noDatasetsFound",
    notPublishedKey: "repoList.noDatasetsPublished",
    icon: Database,
    basePath: "/datasets",
  },
  model: {
    titleKey: "repoList.models.title",
    blurbKey: "repoList.models.blurb",
    notFoundKey: "repoList.noModelsFound",
    notPublishedKey: "repoList.noModelsPublished",
    icon: Boxes,
    basePath: "/models",
  },
};

export async function RepoListPage({
  kind,
  searchParams,
  author,
  basePath,
  showHeading = true,
  preserveParams = [],
  experiment,
}: {
  kind: RepoKind;
  searchParams: Promise<RepoListSearch>;
  /** Restrict the listing to one namespace, for a namespace's own page. */
  author?: string;
  /** Where filter and paging links point. Defaults to /datasets or /models. */
  basePath?: string;
  /** Hidden when the host page (a namespace profile) supplies its own title. */
  showHeading?: boolean;
  /**
   * Query keys that are not filters and must survive "clear filters" -- the
   * namespace page's `tab`, which otherwise falls back to the Models tab.
   */
  preserveParams?: string[];
  /**
   * Tri-state experiment filter forwarded to the API; omitted lists both.
   * The namespace page's Datasets tab passes `false` so the listing agrees
   * with `num_datasets`, which excludes experiment repositories.
   */
  experiment?: boolean;
}) {
  const [sp, t] = await Promise.all([searchParams, getT()]);
  const offset = Number(sp.offset ?? 0) || 0;
  const sort: Sort = (sp.sort as Sort | undefined) ?? "updated";
  const search = sp.search ?? sp.q ?? "";
  const tags = listSearchTags(sp);
  const baseOnly = listFlagOn(sp.base_only);
  const archived = listTriState(sp.archived);
  const copy = COPY[kind];
  const listBase = basePath ?? copy.basePath;
  // "Clear filters" removes every filter, not the sort order: sort decides
  // how the (unfiltered) results are arranged, it doesn't narrow which ones
  // match, and a chip's own × already carries `sort` across via
  // repoListHref. Before this, "Clear filters" and a chip's × both claimed to
  // do the same thing — drop everything but this one filter's worth of
  // narrowing — while disagreeing on whether sort counted as a filter.
  const clearHref = withPreservedParams(
    listBase,
    sp,
    Array.from(new Set([...preserveParams, "sort"])),
  );
  const hasFilters =
    search !== "" ||
    tags.length > 0 ||
    Boolean(sp.license) ||
    Boolean(sp.task) ||
    Boolean(sp.base_model) ||
    Boolean(sp.relation) ||
    Boolean(sp.dataset) ||
    baseOnly ||
    archived !== undefined;

  // Forward the tf_session cookie so the request is authenticated
  // in the list alongside public ones (see lib/server-auth.ts).
  const result = await listRepos(
    {
      kind,
      author,
      search: search || undefined,
      tags: tags.length > 0 ? tags : undefined,
      license: sp.license,
      task: sp.task,
      base_model: sp.base_model,
      relation: sp.relation,
      dataset: sp.dataset,
      // Only ever sent when on: `base_only=false` is the same as no filter,
      // and leaving it out keeps the query string readable.
      base_only: baseOnly || undefined,
      experiment,
      archived,
      sort,
      limit: LIMIT,
      offset,
    },
    { headers: await authHeaders() },
  );
  const facets = result.ok ? result.data.facets : EMPTY_FACETS;
  // Told to the sidebar so it never prints a fabricated "0" next to a
  // selected filter when the listing request itself failed (DESIGN.md §9-1)
  // — the row still has to stay so the filter is removable (§8-4), but the
  // count under a failed request isn't a real zero, it's "unknown".
  const facetsAvailable = result.ok;
  // Distinguishes "nothing matches" from "you've paged past the end of a
  // non-empty list" (e.g. a bookmarked ?offset=, a browser back after
  // someone else deleted repos, or Prev/Next racing a shrinking count).
  // The generic notFoundKey/noMatches copy below would otherwise claim
  // there is nothing here at all, which isn't true.
  const total = result.ok ? result.data.total : 0;
  const offsetOutOfRange = result.ok && offset > 0 && total > 0 && offset >= total;

  return (
    <div className="flex flex-col gap-6">
      {showHeading && (
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t(copy.titleKey)}</h1>
          <p className="mt-1 text-sm text-fg-subtle">{t(copy.blurbKey)}</p>
        </div>
      )}

      <Suspense>
        <RepoListFilters basePath={listBase} />
      </Suspense>

      <div className="flex flex-col gap-6 lg:flex-row lg:items-start">
        <Suspense>
          <RepoFacetSidebar
            basePath={listBase}
            clearHref={clearHref}
            kind={kind}
            facets={facets}
            facetsAvailable={facetsAvailable}
          />
        </Suspense>

        <div className="flex min-w-0 flex-1 flex-col gap-4">
          {/* Above the results, not only inside the EmptyState: as soon as one
              repository matches, the empty state is gone and with it the only
              way to drop a filter without walking back to the sidebar. */}
          {result.ok && (
            <RepoListActiveFilters
              basePath={listBase}
              sp={sp}
              total={result.data.total}
              clearHref={clearHref}
            />
          )}
          {!result.ok ? (
            <ErrorState
              title={t("ui.errorStateTitle")}
              message={errorMessage(t, result)}
              hint={t("repoList.backendHint")}
            />
          ) : result.data.items.length === 0 ? (
            offsetOutOfRange ? (
              <EmptyState
                icon={copy.icon}
                title={t("ui.pagination.outOfRangeTitle")}
                description={t("ui.pagination.outOfRangeDescription")}
                action={
                  <Link
                    href={repoListHref(listBase, sp, { offset: 0 })}
                    className={buttonClass({ variant: "secondary", size: "sm" })}
                  >
                    {t("ui.pagination.backToFirstPage")}
                  </Link>
                }
              />
            ) : (
              <EmptyState
                icon={copy.icon}
                title={hasFilters ? t("repoList.noMatches") : t(copy.notFoundKey)}
                description={hasFilters ? t("repoList.tryRemovingFilter") : t(copy.notPublishedKey)}
                action={
                  hasFilters ? (
                    <Link
                      href={clearHref}
                      className={buttonClass({ variant: "secondary", size: "sm" })}
                    >
                      {t("repoList.clearFilters")}
                    </Link>
                  ) : undefined
                }
              />
            )
          ) : (
            <>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {result.data.items.map((repo) => (
                  <RepoCard key={repo.id} repo={repo} />
                ))}
              </div>
              <Pagination
                offset={offset}
                limit={LIMIT}
                total={result.data.total}
                buildHref={(o) => repoListHref(listBase, sp, { offset: o })}
              />
            </>
          )}
        </div>
      </div>
    </div>
  );
}

/** `base` plus only the `keys` from the current query (for "clear filters"). */
function withPreservedParams(base: string, sp: RepoListSearch, keys: string[]): string {
  const params = new URLSearchParams();
  for (const key of keys) {
    const value = (sp as Record<string, string | string[] | undefined>)[key];
    if (value === undefined) continue;
    for (const v of Array.isArray(value) ? value : [value]) params.append(key, v);
  }
  const qs = params.toString();
  return qs ? `${base}?${qs}` : base;
}
