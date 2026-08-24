import type { Metadata } from "next";
import { titleMetadata } from "@/app/page-metadata";
import { RepoTree } from "@/components/repo-pages/repo-tree";
import { getT } from "@/lib/i18n/server";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export async function generateMetadata({
  params,
}: {
  params: Promise<{ ns: string; name: string; rev: string; path?: string[] }>;
}): Promise<Metadata> {
  const [{ ns, name, path }, t] = await Promise.all([params.then(decodeRouteParams), getT()]);
  // The directory being browsed says more than the revision does; at the
  // repository root there is none, so fall back to the name of the tab.
  return titleMetadata(`${ns}/${name}`, (path ?? []).join("/") || t("meta.files"));
}

export default async function DatasetTreePage({
  params,
}: {
  params: Promise<{ ns: string; name: string; rev: string; path?: string[] }>;
}) {
  const { ns, name, rev, path } = decodeRouteParams(await params);
  return <RepoTree kind="dataset" ns={ns} name={name} rev={rev} path={path ?? []} />;
}
