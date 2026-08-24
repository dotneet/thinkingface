import type { Metadata } from "next";
import { titleMetadata } from "@/app/page-metadata";
import { OrgAuditLog } from "@/components/orgs/org-audit-log";
import { getT } from "@/lib/i18n/server";
import { decodeRouteParams } from "@/lib/paths";

export const dynamic = "force-dynamic";

export async function generateMetadata({
  params,
}: {
  params: Promise<{ name: string }>;
}): Promise<Metadata> {
  const [{ name }, t] = await Promise.all([params.then(decodeRouteParams), getT()]);
  return titleMetadata(name, t("meta.settings"), t("meta.auditLog"));
}

export default async function OrgAuditLogPage({ params }: { params: Promise<{ name: string }> }) {
  const [{ name }, t] = await Promise.all([params, getT()]);
  return (
    <div className="flex flex-col gap-6">
      <div>
        <h2 className="text-sm font-semibold text-fg">{t("org.settings.auditLog.title")}</h2>
        <p className="mt-1 text-sm text-fg-subtle">{t("org.settings.auditLog.description")}</p>
      </div>
      <OrgAuditLog org={name} />
    </div>
  );
}
