import { RepoEdit } from "@/components/repo-pages/repo-edit";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export default async function ModelEditPage({
  params,
  searchParams,
}: {
  params: Promise<{ ns: string; name: string; rev: string; path: string[] }>;
  searchParams: Promise<{ new?: string }>;
}) {
  const { ns, name, rev, path } = decodeRouteParams(await params);
  // `?new=1` comes from the tree's "Create a new file" prompt: it is what
  // tells the editor to open an empty file at a path that does not exist yet
  // instead of 404ing.
  const isNew = (await searchParams).new === "1";
  return <RepoEdit kind="model" ns={ns} name={name} rev={rev} path={path} isNew={isNew} />;
}
