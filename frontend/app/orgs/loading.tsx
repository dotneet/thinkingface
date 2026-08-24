import { CardGridSkeleton } from "@/components/repo/repo-list-skeleton";
import { Skeleton } from "@/components/ui/skeleton";

/** First paint for the organization directory. */
export default function OrgsLoading() {
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex flex-col gap-2">
          <Skeleton className="h-8 w-56" />
          <Skeleton className="h-4 w-72" />
        </div>
        <Skeleton className="h-9 w-40" />
      </div>
      <Skeleton className="h-9 w-full max-w-xl" />
      {/* The count / active-filter row, whose height the real page reserves */}
      <Skeleton className="h-7 w-40" />
      <CardGridSkeleton />
    </div>
  );
}
