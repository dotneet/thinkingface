import { CardGridSkeleton } from "@/components/repo/repo-list-skeleton";
import { Skeleton } from "@/components/ui/skeleton";

/** First paint for an experiment repository's project list. */
export default function ExperimentRepoLoading() {
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Skeleton className="h-5 w-56" />
        <Skeleton className="h-8 w-72" />
        <Skeleton className="h-4 w-96" />
      </div>
      <CardGridSkeleton />
    </div>
  );
}
