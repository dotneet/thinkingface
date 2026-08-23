import { RepoListPage } from "@/components/repo/repo-list-page";
import type { RepoListSearch } from "@/lib/repos";

export const dynamic = "force-dynamic";

export default function DatasetsPage({ searchParams }: { searchParams: Promise<RepoListSearch> }) {
  return <RepoListPage kind="dataset" searchParams={searchParams} />;
}
