import { Skeleton } from "@/components/ui/skeleton";

/**
 * The grid of cards every directory listing ends in (repositories,
 * organizations). Mirrors the real grid's breakpoints so the first paint has
 * the same number of columns as the content that replaces it.
 */
export function CardGridSkeleton({ count = 6 }: { count?: number }) {
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
      {Array.from({ length: count }, (_, i) => `card-${i}`).map((key) => (
        <Skeleton key={key} className="h-28 w-full" />
      ))}
    </div>
  );
}

/**
 * First paint for `/models` and `/datasets` (DESIGN.md §4): heading, the
 * search + sort row, then the facet sidebar beside the result grid —
 * `RepoListPage`'s layout, at the same widths, so nothing jumps when the
 * listing arrives (§8).
 */
export function RepoListSkeleton() {
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-4 w-80" />
      </div>

      {/* RepoListFilters: search box + sort select */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
        <Skeleton className="h-9 flex-1" />
        <Skeleton className="h-9 w-40" />
      </div>

      <div className="flex flex-col gap-6 lg:flex-row lg:items-start">
        {/* RepoFacetSidebar */}
        <div className="flex w-full flex-col gap-4 lg:w-64 lg:shrink-0">
          {Array.from({ length: 3 }, (_, i) => `facet-${i}`).map((key) => (
            <Skeleton key={key} className="h-32 w-full" />
          ))}
        </div>

        <div className="flex min-w-0 flex-1 flex-col gap-4">
          {/* The active-filter / count row, which is always present */}
          <Skeleton className="h-7 w-40" />
          <CardGridSkeleton />
        </div>
      </div>
    </div>
  );
}
