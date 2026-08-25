import type { Metadata } from "next";
import { titleMetadata } from "@/app/page-metadata";
import { RepoCommitDiff } from "@/components/repo-pages/repo-commit-diff";
import { getT } from "@/lib/i18n/server";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export async function generateMetadata({
  params,
}: {
  params: Promise<{ ns: string; name: string; rev: string }>;
}): Promise<Metadata> {
  const [{ ns, name, rev }, t] = await Promise.all([params.then(decodeRouteParams), getT()]);
  // The revision is an identifier, so it goes in verbatim, abbreviated the
  // way every other SHA in the UI is (app/page-metadata.ts).
  return titleMetadata(`${ns}/${name}`, `${t("repo.diff.metaTitle")} ${rev.slice(0, 7)}`);
}

export default async function ModelCommitDiffPage({
  params,
}: {
  params: Promise<{ ns: string; name: string; rev: string }>;
}) {
  const { ns, name, rev } = decodeRouteParams(await params);
  return <RepoCommitDiff kind="model" ns={ns} name={name} rev={rev} />;
}
