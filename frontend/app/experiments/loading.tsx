import { CardGridSkeleton } from "@/components/repo/repo-list-skeleton";
import { Skeleton } from "@/components/ui/skeleton";

/** First paint for the experiment repository listing. */
export default function ExperimentsLoading() {
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-4 w-80" />
      </div>
      <Skeleton className="h-9 w-full max-w-xl" />
      {/* The count / active-filter row, whose height the real page reserves */}
      <Skeleton className="h-7 w-40" />
      <CardGridSkeleton />
    </div>
  );
}
