import type { Metadata } from "next";
import { titleMetadata } from "@/app/page-metadata";
import { RepoOverview } from "@/components/repo-pages/repo-overview";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export async function generateMetadata({
  params,
}: {
  params: Promise<{ ns: string; name: string }>;
}): Promise<Metadata> {
  const { ns, name } = decodeRouteParams(await params);
  return titleMetadata(`${ns}/${name}`);
}

export default async function DatasetOverviewPage({
  params,
}: {
  params: Promise<{ ns: string; name: string }>;
}) {
  const { ns, name } = decodeRouteParams(await params);
  return <RepoOverview kind="dataset" ns={ns} name={name} />;
}
