import type { Metadata } from "next";
import { titleMetadata } from "@/app/page-metadata";
import { RepoViewer } from "@/components/repo-pages/repo-viewer";
import { getT } from "@/lib/i18n/server";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export async function generateMetadata({
  params,
}: {
  params: Promise<{ ns: string; name: string; rev: string; path: string[] }>;
}): Promise<Metadata> {
  const [{ ns, name, path }, t] = await Promise.all([params.then(decodeRouteParams), getT()]);
  return titleMetadata(`${ns}/${name}`, t("meta.viewer"), path.join("/"));
}

export default async function ModelViewerPage({
  params,
}: {
  params: Promise<{ ns: string; name: string; rev: string; path: string[] }>;
}) {
  const { ns, name, rev, path } = decodeRouteParams(await params);
  return <RepoViewer kind="model" ns={ns} name={name} rev={rev} path={path} />;
}
