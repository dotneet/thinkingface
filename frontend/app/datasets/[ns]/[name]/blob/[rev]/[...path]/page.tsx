import type { Metadata } from "next";
import { titleMetadata } from "@/app/page-metadata";
import { RepoBlob } from "@/components/repo-pages/repo-blob";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export async function generateMetadata({
  params,
}: {
  params: Promise<{ ns: string; name: string; rev: string; path: string[] }>;
}): Promise<Metadata> {
  const { ns, name, path } = decodeRouteParams(await params);
  return titleMetadata(`${ns}/${name}`, path.join("/"));
}

export default async function DatasetBlobPage({
  params,
}: {
  params: Promise<{ ns: string; name: string; rev: string; path: string[] }>;
}) {
  const { ns, name, rev, path } = decodeRouteParams(await params);
  return <RepoBlob kind="dataset" ns={ns} name={name} rev={rev} path={path} />;
}
