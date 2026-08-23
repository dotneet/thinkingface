import { RepoOverview } from "@/components/repo-pages/repo-overview";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export default async function DatasetOverviewPage({
  params,
}: {
  params: Promise<{ ns: string; name: string }>;
}) {
  const { ns, name } = decodeRouteParams(await params);
  return <RepoOverview kind="dataset" ns={ns} name={name} />;
}
