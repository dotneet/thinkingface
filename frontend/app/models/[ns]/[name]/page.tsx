import { RepoOverview } from "@/components/repo-pages/repo-overview";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export default async function ModelOverviewPage({
  params,
}: {
  params: Promise<{ ns: string; name: string }>;
}) {
  const { ns, name } = decodeRouteParams(await params);
  return <RepoOverview kind="model" ns={ns} name={name} />;
}
