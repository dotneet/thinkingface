import { OrganizationsManager } from "@/components/settings/organizations-manager";
import { getT } from "@/lib/i18n/server";

export const dynamic = "force-dynamic";

export default async function MyOrganizationsPage() {
  const t = await getT();
  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">
          {t("settings.organizations.title")}
        </h1>
        <p className="mt-1 text-sm text-fg-subtle">{t("settings.organizations.description")}</p>
      </div>
      <OrganizationsManager />
    </div>
  );
}
