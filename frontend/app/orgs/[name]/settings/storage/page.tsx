import { StorageUsage } from "@/components/settings/storage-usage";
import { getT } from "@/lib/i18n/server";
import { orgSettingsHref } from "@/lib/orgs";

export const dynamic = "force-dynamic";

export default async function OrgStorageSettingsPage({
  params,
}: {
  params: Promise<{ name: string }>;
}) {
  const [{ name }, t] = await Promise.all([params, getT()]);
  return (
    <div className="flex flex-col gap-6">
      <div>
        <h2 className="text-sm font-semibold text-fg">{t("org.settings.storage.title")}</h2>
        <p className="mt-1 text-sm text-fg-subtle">{t("org.settings.storage.description")}</p>
      </div>
      <StorageUsage
        namespace={name}
        loginNext={`${orgSettingsHref(name)}/storage`}
        emptyTitle={t("org.settings.storage.emptyTitle")}
        emptyDescription={t("org.settings.storage.emptyDescription")}
      />
    </div>
  );
}
