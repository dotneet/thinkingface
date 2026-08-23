import { FlaskConical, Search } from "lucide-react";
import Link from "next/link";
import { buttonClass } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Input } from "@/components/ui/field";
import { FilterChip } from "@/components/ui/filter-chip";
import { Pagination } from "@/components/ui/pagination";
import { TimeText } from "@/components/ui/time-text";
import { errorMessage } from "@/lib/api-error-message";
import { listExperiments } from "@/lib/experiments";
import { formatNumber } from "@/lib/format";
import { getT } from "@/lib/i18n/server";
import { authHeaders } from "@/lib/server-auth";

export const dynamic = "force-dynamic";

const LIMIT = 30;

type PageSearchParams = { search?: string; offset?: string };

export default async function ExperimentsPage({
  searchParams,
}: {
  searchParams: Promise<PageSearchParams>;
}) {
  const [sp, t] = await Promise.all([searchParams, getT()]);
  const search = sp.search ?? "";
  const offset = Number(sp.offset ?? 0) || 0;

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
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {result.data.items.map((item) => (
              <Link
                key={item.full_name}
                href={`/experiments/${encodeURIComponent(item.namespace)}/${encodeURIComponent(item.name)}`}
                className="flex flex-col gap-2 rounded-lg border border-border bg-bg-raised p-4 transition-colors hover:border-border-strong hover:bg-bg-hover"
              >
                <div className="flex items-center gap-1.5 text-sm font-medium">
                  <FlaskConical size={14} className="text-fg-subtle" />
                  {item.full_name}
                </div>
                <div className="flex items-center gap-3 text-xs font-medium text-fg-subtle">
                  <span className="tabular-nums">
                    {t(
                      item.num_projects === 1
                        ? "experiments.index.projectsOne"
                        : "experiments.index.projectsOther",
                      { count: formatNumber(item.num_projects) },
                    )}
                  </span>
                  <TimeText iso={item.updated_at} style="relative" />
                </div>
              </Link>
            ))}
          </div>
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
