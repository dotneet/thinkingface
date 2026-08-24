import type { Metadata } from "next";
import { titleMetadata } from "@/app/page-metadata";
import { WebhooksManager } from "@/components/settings/webhooks-manager";
import { getT } from "@/lib/i18n/server";
import { orgSettingsHref } from "@/lib/orgs";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export async function generateMetadata({
  params,
}: {
  params: Promise<{ name: string }>;
}): Promise<Metadata> {
  const [{ name }, t] = await Promise.all([params.then(decodeRouteParams), getT()]);
  return titleMetadata(name, t("meta.settings"), t("meta.webhooks"));
}

export default async function OrgWebhooksSettingsPage({
  params,
}: {
  params: Promise<{ name: string }>;
}) {
  const [{ name }, t] = await Promise.all([params, getT()]);
  return (
    <div className="flex flex-col gap-6">
      <div>
        <h2 className="text-sm font-semibold text-fg">{t("org.settings.webhooks.title")}</h2>
        <p className="mt-1 text-sm text-fg-subtle">{t("org.settings.webhooks.description")}</p>
      </div>
      <WebhooksManager namespace={name} loginNext={`${orgSettingsHref(name)}/webhooks`} />
    </div>
  );
}
