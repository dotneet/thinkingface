import { RepoPageSkeleton } from "@/components/repo/repo-page-skeleton";
import { Skeleton } from "@/components/ui/skeleton";

/** First paint for the repository settings forms, under the shared repository chrome. */
export default function DatasetSettingsLoading() {
  return (
    <RepoPageSkeleton>
      {Array.from({ length: 3 }, (_, i) => `card-${i}`).map((key) => (
        <Skeleton key={key} className="h-44 w-full" />
      ))}
    </RepoPageSkeleton>
  );
}
