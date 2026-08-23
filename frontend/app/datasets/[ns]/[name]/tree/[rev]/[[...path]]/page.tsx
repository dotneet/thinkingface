import { RepoTree } from "@/components/repo-pages/repo-tree";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export default async function DatasetTreePage({
  params,
}: {
  params: Promise<{ ns: string; name: string; rev: string; path?: string[] }>;
}) {
  const { ns, name, rev, path } = decodeRouteParams(await params);
  return <RepoTree kind="dataset" ns={ns} name={name} rev={rev} path={path ?? []} />;
}
