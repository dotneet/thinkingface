import { FileNavSkeleton, RepoPageSkeleton } from "@/components/repo/repo-page-skeleton";
import { Skeleton } from "@/components/ui/skeleton";

/** First paint for a file and its preview, under the shared repository chrome. */
export default function DatasetBlobLoading() {
  return (
    <RepoPageSkeleton>
      <FileNavSkeleton />
      {/* The size / LFS badge row, then the last-commit bar above the preview */}
      <Skeleton className="h-5 w-40" />
      <Skeleton className="h-12 w-full" />
      <Skeleton className="h-96 w-full" />
    </RepoPageSkeleton>
  );
}
