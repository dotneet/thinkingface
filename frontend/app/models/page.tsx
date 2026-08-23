import { RepoListPage } from "@/components/repo/repo-list-page";
import type { RepoListSearch } from "@/lib/repos";

export const dynamic = "force-dynamic";

export default function ModelsPage({ searchParams }: { searchParams: Promise<RepoListSearch> }) {
  return <RepoListPage kind="model" searchParams={searchParams} />;
}
