import {
  FileNavSkeleton,
  RepoPageSkeleton,
  TableSkeleton,
} from "@/components/repo/repo-page-skeleton";

/** First paint for the file listing, under the shared repository chrome. */
export default function DatasetTreeLoading() {
  return (
    <RepoPageSkeleton>
      <FileNavSkeleton />
      <TableSkeleton rows={10} />
    </RepoPageSkeleton>
  );
}
