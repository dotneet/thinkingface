import { FlaskConical, Search } from "lucide-react";
import type { Metadata } from "next";
import Link from "next/link";

import { titleMetadata } from "@/app/page-metadata";
import { badgeClass } from "@/components/ui/badge";
import { buttonClass } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Input } from "@/components/ui/field";
import { FilterChip } from "@/components/ui/filter-chip";
import { Pagination } from "@/components/ui/pagination";
import { TimeText } from "@/components/ui/time-text";
import { errorMessage } from "@/lib/api-error-message";
import { previewList } from "@/lib/exp-list-status";
import { listExperiments } from "@/lib/experiments";
import { formatNumber } from "@/lib/format";
import { getT } from "@/lib/i18n/server";
import { parseOffset } from "@/lib/pagination";
import { authHeaders } from "@/lib/server-auth";

export const dynamic = "force-dynamic";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getT();
  return titleMetadata(t("meta.experiments"));
}

const LIMIT = 30;

/** Project chips shown per repository row before "+N". */
const PROJECT_PREVIEW = 3;

type PageSearchParams = { search?: string; offset?: string };

export default async function ExperimentsPage({
  searchParams,
}: {
  searchParams: Promise<PageSearchParams>;
}) {
  const [sp, t] = await Promise.all([searchParams, getT()]);
  const search = sp.search ?? "";
  const offset = parseOffset(sp.offset);

  // Forward the tf_session cookie so the experiment repos the viewer
  // can see resolve instead of 404ing (see lib/server-auth.ts). The backend
  // caps this endpoint at 100 results (backend/internal/api/experiments.go),
  // so `search` and paging are what let a bookmark-worthy repository past
  // the first page stay reachable.
  const result = await listExperiments(
    { search: search || undefined, limit: LIMIT, offset },
    { headers: await authHeaders() },
  );
  // Same distinction as RepoListPage / OrgsDirectoryPage: "no experiment
  // repositories exist / match" reads differently from "you've paged past
  // the end of a non-empty list", so a bookmarked/stale ?offset= doesn't
  // read as "everything is gone".
  const total = result.ok ? result.data.total : 0;
  const offsetOutOfRange = result.ok && offset > 0 && total > 0 && offset >= total;
  const firstPageHref = search
    ? `/experiments?search=${encodeURIComponent(search)}`
    : "/experiments";

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("experiments.index.title")}</h1>
        <p className="mt-1 text-sm text-fg-subtle">{t("experiments.index.description")}</p>
      </div>

      {/* Plain GET form rather than a client component: this page has no
          other file it's allowed to add a "use client" search box to (see
          the parallel-work split for this change), and a native form gives
          the same search=/offset-reset behaviour as OrgSearch without
          needing one. */}
      <form action="/experiments" method="get" className="flex max-w-xl">
        <div className="relative flex-1">
          <Search
            size={15}
            className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-fg-subtle"
          />
          <Input
            name="search"
            defaultValue={search}
            // `type="text"`, not `type="search"`: this is a plain GET form
            // with no client handler, and the browser's own × empties the
            // field without submitting — leaving the box looking cleared
            // while the URL and results stayed on the old term. The
            // FilterChip below is the clear control instead. A client
            // component would use `SearchInput` (ui/search-input.tsx, §9).
            type="text"
            enterKeyHint="search"
            placeholder={t("experiments.index.searchPlaceholder")}
            aria-label={t("experiments.index.searchPlaceholder")}
            className="pl-8 pr-3 text-sm"
          />
        </div>
      </form>

      {/* Always rendered, search or not (DESIGN.md §8): it is the anchor the
          result grid sits under, so mounting it only while a search is active
          would push the first row of cards down at the moment the user runs
          one. It also carries the removal control the EmptyState below can no
          longer offer once a single repository matches. */}
      {/* `min-h-7` so a chip appearing does not make the row taller than the
          bare count did — the chip is 26px against the count line's 20px, and
          without the floor the whole result grid slid 6px on every search. */}
      <div className="flex min-h-7 flex-wrap items-center gap-2">
        {/* Only with a successful response: `total` falls back to 0 on a
            failed request, and "0 …" above an ErrorState reads as an empty
            list rather than a load failure. The row itself (and its height)
            stays either way, so nothing moves. */}
        {result.ok && (
          <span className="mr-1 shrink-0 text-sm font-medium tabular-nums text-fg-subtle">
            {t(total === 1 ? "experiments.index.countOne" : "experiments.index.countOther", {
              count: formatNumber(total),
            })}
          </span>
        )}
        {search && (
          <FilterChip
            label={t("experiments.index.search")}
            value={search}
            href="/experiments"
            removeLabel={t("experiments.index.removeSearchAria", { value: search })}
          />
        )}
      </div>

      {!result.ok ? (
        <ErrorState
          title={t("experiments.errorTitle")}
          message={errorMessage(t, result)}
          hint={t("experiments.index.errorHint")}
        />
      ) : result.data.items.length === 0 ? (
        offsetOutOfRange ? (
          <EmptyState
            icon={FlaskConical}
            title={t("ui.pagination.outOfRangeTitle")}
            description={t("ui.pagination.outOfRangeDescription")}
            action={
              <Link
                href={firstPageHref}
                className={buttonClass({ variant: "secondary", size: "sm" })}
              >
                {t("ui.pagination.backToFirstPage")}
              </Link>
            }
          />
        ) : (
          <EmptyState
            icon={FlaskConical}
            title={
              search ? t("experiments.index.noMatchesTitle") : t("experiments.index.emptyTitle")
            }
            description={
              search
                ? t("experiments.index.noMatchesDescription")
                : t("experiments.index.emptyDescription")
            }
            action={
              search ? (
                <Link
                  href="/experiments"
                  className={buttonClass({ variant: "secondary", size: "sm" })}
                >
                  {t("experiments.index.clearSearch")}
                </Link>
              ) : undefined
            }
          />
        )
      ) : (
        <>
          {/* A list, not a card grid: a row reads left to right — repository,
              then its projects as direct links, then when it last changed —
              and the names line up for scanning, which matters when most of
              them share a prefix (`e2e-…`). The whole row opens the
              repository (a stretched link); the project chips sit above it
              and go straight to that project. */}
          <ul className="flex flex-col divide-y divide-border overflow-hidden rounded-lg border border-border bg-bg-raised">
            {result.data.items.map((item) => {
              const repoHref = `/experiments/${encodeURIComponent(item.namespace)}/${encodeURIComponent(item.name)}`;
              const { shown, hidden } = previewList(item.projects ?? [], PROJECT_PREVIEW);
              return (
                <li
                  key={item.full_name}
                  className="relative flex flex-wrap items-center gap-x-4 gap-y-1.5 px-4 py-2.5 transition-colors hover:bg-bg-hover lg:flex-nowrap"
                >
                  <Link
                    href={repoHref}
                    className="flex min-w-0 flex-1 items-center gap-1.5 text-sm font-medium text-fg after:absolute after:inset-0 after:content-[''] hover:underline lg:w-80 lg:flex-none"
                  >
                    <FlaskConical size={14} className="shrink-0 text-fg-subtle" />
                    <span className="truncate" title={item.full_name}>
                      {item.full_name}
                    </span>
                  </Link>
                  {/* Below lg the chips drop to a second line of their own
                      (order-last + full width); from lg they sit between the
                      name and the meta on one line. */}
                  <div
                    className="order-last flex w-full min-w-0 items-center gap-1.5 overflow-hidden lg:order-none lg:w-auto lg:flex-1"
                    aria-label={t("experiments.listPage.projectsAria", { repo: item.full_name })}
                  >
                    {shown.map((name) => (
                      <Link
                        key={name}
                        href={`${repoHref}/${encodeURIComponent(name)}`}
                        title={name}
                        className={badgeClass({
                          className:
                            "relative z-10 max-w-[11rem] shrink-0 truncate hover:border-border-strong hover:text-fg",
                        })}
                      >
                        <span className="truncate">{name}</span>
                      </Link>
                    ))}
                    {hidden > 0 && (
                      <span
                        className="shrink-0 text-xs font-medium tabular-nums text-fg-subtle"
                        title={t("experiments.listPage.moreProjectsTitle", {
                          count: formatNumber(hidden),
                        })}
                      >
                        {t("experiments.listPage.moreProjects", { count: formatNumber(hidden) })}
                      </span>
                    )}
                  </div>
                  <div className="flex shrink-0 items-center gap-3 text-xs font-medium whitespace-nowrap text-fg-subtle lg:order-last">
                    {/* The chips already show the projects on a phone; the
                        count is for the wide layout, where "+N" hides some. */}
                    <span className="hidden tabular-nums sm:inline">
                      {t(
                        item.num_projects === 1
                          ? "experiments.index.projectsOne"
                          : "experiments.index.projectsOther",
                        { count: formatNumber(item.num_projects) },
                      )}
                    </span>
                    <TimeText
                      iso={item.updated_at}
                      style="relative"
                      className="text-right sm:w-24"
                    />
                  </div>
                </li>
              );
            })}
          </ul>
          <Pagination
            offset={offset}
            limit={LIMIT}
            total={result.data.total}
            buildHref={(o) => {
              const params = new URLSearchParams();
              if (search) params.set("search", search);
              if (o > 0) params.set("offset", String(o));
              const qs = params.toString();
              return qs ? `/experiments?${qs}` : "/experiments";
            }}
          />
        </>
      )}
    </div>
  );
}
