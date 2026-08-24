import type { Metadata } from "next";
import { titleMetadata } from "@/app/page-metadata";
import { RepoEdit } from "@/components/repo-pages/repo-edit";
import { getT } from "@/lib/i18n/server";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export async function generateMetadata({
  params,
}: {
  params: Promise<{ ns: string; name: string; rev: string; path: string[] }>;
}): Promise<Metadata> {
  const [{ ns, name, path }, t] = await Promise.all([params.then(decodeRouteParams), getT()]);
  return titleMetadata(`${ns}/${name}`, t("meta.edit"), path.join("/"));
}

export default async function DatasetEditPage({
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
  return <RepoEdit kind="dataset" ns={ns} name={name} rev={rev} path={path} isNew={isNew} />;
}
