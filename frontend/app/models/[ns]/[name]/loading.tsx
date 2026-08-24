import { RepoPageSkeleton } from "@/components/repo/repo-page-skeleton";
import { Skeleton, SkeletonLines } from "@/components/ui/skeleton";

/** First paint for the repository card, under the shared repository chrome. */
export default function ModelOverviewLoading() {
  return (
    <RepoPageSkeleton>
      <div className="flex flex-col gap-6 lg:flex-row">
        <div className="min-w-0 flex-1 rounded-lg border border-border bg-bg-raised p-6">
          <SkeletonLines lines={8} />
        </div>
        <div className="flex flex-col gap-4 lg:w-72 lg:shrink-0">
          <Skeleton className="h-36 w-full" />
          <Skeleton className="h-48 w-full" />
        </div>
      </div>
    </RepoPageSkeleton>
  );
}
