import {
  FileNavSkeleton,
  RepoPageSkeleton,
  TableSkeleton,
} from "@/components/repo/repo-page-skeleton";

/** First paint for the commit history, under the shared repository chrome. */
export default function DatasetCommitsLoading() {
  return (
    <RepoPageSkeleton>
      <FileNavSkeleton />
      <TableSkeleton rows={10} />
    </RepoPageSkeleton>
  );
}
