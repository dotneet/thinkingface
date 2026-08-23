import { RepoEdit } from "@/components/repo-pages/repo-edit";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export default async function ModelEditPage({
  params,
}: {
  params: Promise<{ ns: string; name: string; rev: string; path: string[] }>;
}) {
  const { ns, name, rev, path } = decodeRouteParams(await params);
  return <RepoEdit kind="model" ns={ns} name={name} rev={rev} path={path} />;
}
