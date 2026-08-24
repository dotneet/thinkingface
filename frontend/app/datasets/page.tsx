import type { Metadata } from "next";
import { titleMetadata } from "@/app/page-metadata";
import { RepoListPage } from "@/components/repo/repo-list-page";
import { getT } from "@/lib/i18n/server";
import type { RepoListSearch } from "@/lib/repos";

export const dynamic = "force-dynamic";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getT();
  return titleMetadata(t("meta.datasets"));
}

export default function DatasetsPage({ searchParams }: { searchParams: Promise<RepoListSearch> }) {
  return <RepoListPage kind="dataset" searchParams={searchParams} />;
}
