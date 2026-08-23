import { RepoCommits } from "@/components/repo-pages/repo-commits";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export default async function ModelCommitsPage({
  params,
  searchParams,
}: {
  params: Promise<{ ns: string; name: string; rev: string }>;
  searchParams: Promise<{ after?: string; path?: string | string[] }>;
}) {
  const { ns, name, rev } = decodeRouteParams(await params);
  const { after, path } = await searchParams;
  return (
    <RepoCommits
      kind="model"
      ns={ns}
      name={name}
      rev={rev}
      after={after}
      path={Array.isArray(path) ? path[0] : path}
    />
  );
}
