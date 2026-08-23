import { Building2, Plus } from "lucide-react";
import Link from "next/link";
import { Suspense } from "react";
import { OrgCard } from "@/components/orgs/org-card";
import { OrgSearch } from "@/components/orgs/org-search";
import { buttonClass } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { FilterChip } from "@/components/ui/filter-chip";
import { Pagination } from "@/components/ui/pagination";
import { errorMessage } from "@/lib/api-error-message";
import { formatNumber } from "@/lib/format";
import { getT } from "@/lib/i18n/server";
import { listOrgs } from "@/lib/orgs";
import { authHeaders } from "@/lib/server-auth";

export const dynamic = "force-dynamic";

const LIMIT = 30;

type Search = { search?: string; offset?: string };

export default async function OrgsDirectoryPage({
  searchParams,
}: {
  searchParams: Promise<Search>;
}) {
  const [sp, t] = await Promise.all([searchParams, getT()]);
  const search = sp.search ?? "";
  const offset = Number(sp.offset ?? 0) || 0;

  // Forwarded so each row can carry the viewer's own role (lib/server-auth.ts).
  const result = await listOrgs(
    { search: search || undefined, limit: LIMIT, offset },
    { headers: await authHeaders() },
  );
  // Same distinction as RepoListPage (components/repo/repo-list-page.tsx):
  // "no organizations exist / match" is a different situation from "you've
  // paged past the end of a non-empty list", and the two need different copy
  // so a bookmarked/stale ?offset= doesn't read as "everything is gone".
  const total = result.ok ? result.data.total : 0;
  const offsetOutOfRange = result.ok && offset > 0 && total > 0 && offset >= total;
  const firstPageHref = search ? `/orgs?search=${encodeURIComponent(search)}` : "/orgs";

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t("org.directory.title")}</h1>
          <p className="mt-1 text-sm text-fg-subtle">{t("org.directory.blurb")}</p>
        </div>
        <Link href="/orgs/new" className={buttonClass({ variant: "primary" })}>
          <Plus size={15} />
          {t("org.directory.create")}
        </Link>
      </div>

      <div className="flex max-w-xl">
        <Suspense>
          <OrgSearch />
        </Suspense>
      </div>

      {/* Always rendered, search or not (DESIGN.md §8): it is the anchor the
          result grid sits under, so mounting it only while a search is active
          would push the first row of cards down at the moment the user runs
          one. It also carries the removal control the EmptyState below can no
          longer offer once a single organization matches. */}
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
            {t(total === 1 ? "org.directory.countOne" : "org.directory.countOther", {
              count: formatNumber(total),
            })}
          </span>
        )}
        {search && (
          <FilterChip
            label={t("org.directory.search")}
            value={search}
            href="/orgs"
            removeLabel={t("org.directory.removeSearchAria", { value: search })}
          />
        )}
      </div>

      {!result.ok ? (
        <ErrorState
          title={t("org.page.loadFailedTitle")}
          message={errorMessage(t, result)}
          hint={t("org.directory.loadFailedHint")}
        />
      ) : result.data.items.length === 0 ? (
        offsetOutOfRange ? (
          <EmptyState
            icon={Building2}
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
            icon={Building2}
            title={search ? t("org.directory.noMatchesTitle") : t("org.directory.emptyTitle")}
            description={
              search ? t("org.directory.noMatchesDescription") : t("org.directory.emptyDescription")
            }
            action={
              search ? (
                <Link href="/orgs" className={buttonClass({ variant: "secondary", size: "sm" })}>
                  {t("org.directory.clearSearch")}
                </Link>
              ) : (
                <Link href="/orgs/new" className={buttonClass({ variant: "primary", size: "sm" })}>
                  {t("org.directory.create")}
                </Link>
              )
            }
          />
        )
      ) : (
        <>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {result.data.items.map((org) => (
              <OrgCard key={org.name} org={org} />
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
              return qs ? `/orgs?${qs}` : "/orgs";
            }}
          />
        </>
      )}
    </div>
  );
}
