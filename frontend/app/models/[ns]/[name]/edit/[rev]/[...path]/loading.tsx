import { FileNavSkeleton, RepoPageSkeleton } from "@/components/repo/repo-page-skeleton";
import { Skeleton } from "@/components/ui/skeleton";

/** First paint for the file editor, under the shared repository chrome. */
export default function ModelEditLoading() {
  return (
    <RepoPageSkeleton>
      <FileNavSkeleton />
      {/* Edit / preview switch, the textarea, then the commit form */}
      <Skeleton className="h-8 w-48" />
      <Skeleton className="h-96 w-full" />
      <Skeleton className="h-28 w-full" />
    </RepoPageSkeleton>
  );
}
