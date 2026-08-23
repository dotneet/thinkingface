import { RepoSettings } from "@/components/repo-pages/repo-settings";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export default async function DatasetSettingsPage({
  params,
}: {
  params: Promise<{ ns: string; name: string }>;
}) {
  const { ns, name } = decodeRouteParams(await params);
  return <RepoSettings kind="dataset" ns={ns} name={name} />;
}
