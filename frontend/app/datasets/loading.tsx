import { RepoListSkeleton } from "@/components/repo/repo-list-skeleton";

/** First paint for `/datasets` while the listing and its facets are fetched. */
export default function DatasetsLoading() {
  return <RepoListSkeleton />;
}
