import { RepoBlob } from "@/components/repo-pages/repo-blob";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export default async function ModelBlobPage({
  params,
}: {
  params: Promise<{ ns: string; name: string; rev: string; path: string[] }>;
}) {
  const { ns, name, rev, path } = decodeRouteParams(await params);
  return <RepoBlob kind="model" ns={ns} name={name} rev={rev} path={path} />;
}
