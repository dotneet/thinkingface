import { RepoPageSkeleton, TableSkeleton } from "@/components/repo/repo-page-skeleton";
import { Skeleton } from "@/components/ui/skeleton";

/** First paint for the parquet schema and first rows, under the shared repository chrome. */
export default function ModelViewerLoading() {
  return (
    <RepoPageSkeleton>
      {/* The row of parquet-file chips, then the table itself */}
      <div className="flex flex-wrap gap-1.5">
        {Array.from({ length: 3 }, (_, i) => `file-${i}`).map((key) => (
          <Skeleton key={key} className="h-6 w-32 rounded-full" />
        ))}
      </div>
      <TableSkeleton rows={12} />
    </RepoPageSkeleton>
  );
}
