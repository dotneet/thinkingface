import { WebhooksManager } from "@/components/settings/webhooks-manager";
import { getT } from "@/lib/i18n/server";

export const dynamic = "force-dynamic";

export default async function WebhooksPage() {
  const t = await getT();
  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("settings.webhooks.title")}</h1>
        <p className="mt-1 text-sm text-fg-subtle">
          {t("settings.webhooks.description")}
          <code className="mx-1 font-mono text-xs">X-Thinkingface-Signature</code>
          {t("settings.webhooks.descriptionSuffix")}
        </p>
      </div>
      <WebhooksManager />
    </div>
  );
}
