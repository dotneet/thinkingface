import type { Metadata } from "next";
import { titleMetadata } from "@/app/page-metadata";
import { RepoSettings } from "@/components/repo-pages/repo-settings";
import { getT } from "@/lib/i18n/server";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export async function generateMetadata({
  params,
}: {
  params: Promise<{ ns: string; name: string }>;
}): Promise<Metadata> {
  const [{ ns, name }, t] = await Promise.all([params.then(decodeRouteParams), getT()]);
  return titleMetadata(`${ns}/${name}`, t("meta.settings"));
}

export default async function DatasetSettingsPage({
  params,
}: {
  params: Promise<{ ns: string; name: string }>;
}) {
  const { ns, name } = decodeRouteParams(await params);
  return <RepoSettings kind="dataset" ns={ns} name={name} />;
}
