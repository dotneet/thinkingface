import { RepoPageSkeleton } from "@/components/repo/repo-page-skeleton";
import { Skeleton } from "@/components/ui/skeleton";

/**
 * First paint for a commit's diff: the metadata card, then two file blocks.
 * Deliberately not `TableSkeleton` — the content is a stack of panels, and a
 * placeholder shaped like a table would reflow the page once the real thing
 * arrives.
 */
export default function DatasetCommitDiffLoading() {
  return (
    <RepoPageSkeleton>
      <Skeleton className="h-4 w-48" />
      <Skeleton className="h-28 w-full rounded-lg" />
      <Skeleton className="h-48 w-full rounded-lg" />
      <Skeleton className="h-48 w-full rounded-lg" />
    </RepoPageSkeleton>
  );
}
