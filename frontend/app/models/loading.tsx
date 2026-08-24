import { RepoListSkeleton } from "@/components/repo/repo-list-skeleton";

/** First paint for `/models` while the listing and its facets are fetched. */
export default function ModelsLoading() {
  return <RepoListSkeleton />;
}
