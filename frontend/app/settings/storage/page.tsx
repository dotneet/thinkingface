import type { Metadata } from "next";
import { titleMetadata } from "@/app/page-metadata";
import { StorageUsage } from "@/components/settings/storage-usage";
import { getT } from "@/lib/i18n/server";

export const dynamic = "force-dynamic";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getT();
  return titleMetadata(t("meta.settings"), t("meta.storage"));
}

export default async function StorageUsagePage() {
  const t = await getT();
  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("settings.storage.title")}</h1>
        <p className="mt-1 text-sm text-fg-subtle">{t("settings.storage.description")}</p>
      </div>
      <StorageUsage />
    </div>
  );
}
