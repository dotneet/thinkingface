import { ChevronLeft } from "lucide-react";
import Link from "next/link";
import { OrgSettingsNav } from "@/components/orgs/org-settings-nav";
import { buttonClass } from "@/components/ui/button";
import { ErrorState } from "@/components/ui/error-state";
import { isNotFound } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { getT } from "@/lib/i18n/server";
import { namespaceHref } from "@/lib/namespace";
import { canAdminOrg, getOrg } from "@/lib/orgs";
import { authHeaders } from "@/lib/server-auth";

export const dynamic = "force-dynamic";

/**
 * Guard and chrome for every organisation settings screen.
 *
 * The admin check lives here rather than in each page: `children` is only an
 * element until it is rendered, so not returning it means the page component
 * never runs and never fetches. Non-admins get an explicit "admins only"
 * message rather than a 404 — the organisation's existence is public
 * information, and the only way to land here without the role is a stale
 * bookmark (docs/dev/organization-design.md §8.1).
 */
export default async function OrgSettingsLayout({
  children,
  params,
}: {
  children: React.ReactNode;
  params: Promise<{ name: string }>;
}) {
  const [{ name }, t] = await Promise.all([params, getT()]);
  const result = await getOrg(name, { headers: await authHeaders() });

  if (!result.ok) {
    return (
      <ErrorState
        title={isNotFound(result) ? t("org.page.notFoundTitle") : t("org.page.loadFailedTitle")}
        message={
          isNotFound(result) ? t("org.page.notFoundDescription", { name }) : errorMessage(t, result)
        }
        hint={isNotFound(result) ? undefined : t("org.settings.loadFailedHint")}
      />
    );
  }

  const org = result.data.org;
  const label = org.display_name || org.name;

  if (!canAdminOrg(org)) {
    return (
      <ErrorState
        title={t("org.settings.noPermissionTitle")}
        message={t("org.settings.noPermissionMessage", { name: label })}
        action={
          <Link href={namespaceHref(org.name)} className={buttonClass({ variant: "secondary" })}>
            {t("org.settings.backToOrg", { name: label })}
          </Link>
        }
      />
    );
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-1">
        <Link
          href={namespaceHref(org.name)}
          className="flex w-fit items-center gap-1 text-sm text-fg-subtle hover:text-fg hover:underline"
        >
          <ChevronLeft size={14} />
          {t("org.settings.backToOrg", { name: label })}
        </Link>
        <h1 className="text-2xl font-semibold tracking-tight">{t("org.settings.title")}</h1>
        <p className="text-sm text-fg-subtle">{t("org.settings.subtitle", { name: label })}</p>
      </div>

      <div className="flex flex-col gap-6 lg:flex-row lg:items-start">
        <OrgSettingsNav name={org.name} />
        <div className="min-w-0 flex-1">{children}</div>
      </div>
    </div>
  );
}
