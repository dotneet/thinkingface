import { RepoViewer } from "@/components/repo-pages/repo-viewer";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export default async function ModelViewerPage({
  params,
}: {
  params: Promise<{ ns: string; name: string; rev: string; path: string[] }>;
}) {
  const { ns, name, rev, path } = decodeRouteParams(await params);
  return <RepoViewer kind="model" ns={ns} name={name} rev={rev} path={path} />;
}
