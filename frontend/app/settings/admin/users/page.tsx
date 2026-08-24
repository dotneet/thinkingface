import Link from "next/link";
import { AdminUsersManager } from "@/components/settings/admin-users-manager";
import { buttonClass } from "@/components/ui/button";
import { ErrorState } from "@/components/ui/error-state";
import { isUnauthorized } from "@/lib/api";
import { getMe } from "@/lib/auth";
import { getT } from "@/lib/i18n/server";
import { authHeaders } from "@/lib/server-auth";

export const dynamic = "force-dynamic";

/**
 * Site administration: every account on the instance
 * (docs/dev/api-contract.md §1.3).
 *
 * The gate is here as well as on the backend — which answers 403 to anyone
 * without `users.is_admin` — so a non-administrator gets a sentence rather
 * than a screen that renders and then fails one fetch later. The identity is
 * read server-side with `authHeaders()`, since `credentials: "include"` does
 * nothing in a Server Component (CLAUDE.md invariant 2).
 */
export default async function AdminUsersPage() {
  const t = await getT();
  const me = await getMe({ headers: await authHeaders() });

  if (!me.ok) {
    return (
      <ErrorState
        title={
          isUnauthorized(me)
            ? t("settings.account.loginRequiredTitle")
            : t("settings.adminUsers.loadFailed")
        }
        message={
          isUnauthorized(me)
            ? t("settings.adminUsers.errors.loginRequired")
            : t("settings.adminUsers.loadFailedHint")
        }
        action={
          isUnauthorized(me) ? (
            <Link
              href="/login?next=/settings/admin/users"
              className={buttonClass({ variant: "primary" })}
            >
              {t("settings.account.login")}
            </Link>
          ) : undefined
        }
      />
    );
  }

  if (!me.data.user.is_admin) {
    return (
      <ErrorState
        title={t("settings.adminUsers.accessDeniedTitle")}
        message={t("settings.adminUsers.accessDeniedMessage")}
      />
    );
  }

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("settings.adminUsers.title")}</h1>
        <p className="mt-1 text-sm text-fg-subtle">{t("settings.adminUsers.description")}</p>
        <p className="mt-1 text-xs font-medium text-fg-subtle">
          {t("settings.adminUsers.seededAdminNote")}
        </p>
      </div>
      <AdminUsersManager viewer={me.data.user.username} />
    </div>
  );
}
