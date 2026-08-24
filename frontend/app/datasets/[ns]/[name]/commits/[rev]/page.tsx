import type { Metadata } from "next";
import { titleMetadata } from "@/app/page-metadata";
import { RepoCommits } from "@/components/repo-pages/repo-commits";
import { getT } from "@/lib/i18n/server";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export async function generateMetadata({
  params,
}: {
  params: Promise<{ ns: string; name: string; rev: string }>;
}): Promise<Metadata> {
  const [{ ns, name }, t] = await Promise.all([params.then(decodeRouteParams), getT()]);
  return titleMetadata(`${ns}/${name}`, t("meta.commits"));
}

export default async function DatasetCommitsPage({
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
      kind="dataset"
      ns={ns}
      name={name}
      rev={rev}
      after={after}
      path={Array.isArray(path) ? path[0] : path}
    />
  );
}
