import type { Metadata } from "next";
import Link from "next/link";
import { titleMetadata } from "@/app/page-metadata";
import { AdminSyncJobsManager } from "@/components/settings/admin-sync-jobs-manager";
import { buttonClass } from "@/components/ui/button";
import { ErrorState } from "@/components/ui/error-state";
import { isUnauthorized } from "@/lib/api";
import { getMe } from "@/lib/auth";
import { getT } from "@/lib/i18n/server";
import { authHeaders } from "@/lib/server-auth";

export const dynamic = "force-dynamic";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getT();
  return titleMetadata(t("meta.settings"), t("meta.adminSyncJobs"));
}

/**
 * Site administration: the post-push sync jobs that gave up
 * (docs/dev/api-contract.md §1.3, "Failed sync jobs").
 *
 * The gate mirrors /settings/admin/users — enforced by the backend, which
 * answers 403 to anyone without `users.is_admin`, and repeated here so a
 * non-administrator gets a sentence rather than a screen that renders and then
 * fails one fetch later. The identity is read server-side with
 * `authHeaders()`, since `credentials: "include"` does nothing in a Server
 * Component (CLAUDE.md invariant 2).
 */
export default async function AdminSyncJobsPage() {
  const t = await getT();
  const me = await getMe({ headers: await authHeaders() });

  if (!me.ok) {
    return (
      <ErrorState
        title={
          isUnauthorized(me)
            ? t("settings.loginRequiredTitle")
            : t("settings.adminSyncJobs.loadFailed")
        }
        message={
          isUnauthorized(me)
            ? t("settings.adminUsers.errors.loginRequired")
            : t("settings.adminSyncJobs.loadFailedHint")
        }
        action={
          isUnauthorized(me) ? (
            <Link
              href="/login?next=/settings/admin/sync-jobs"
              className={buttonClass({ variant: "primary" })}
            >
              {t("settings.login")}
            </Link>
          ) : undefined
        }
      />
    );
  }

  if (!me.data.user.is_admin) {
    return (
      <ErrorState
        title={t("settings.adminSyncJobs.accessDeniedTitle")}
        message={t("settings.adminSyncJobs.accessDeniedMessage")}
      />
    );
  }

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">
          {t("settings.adminSyncJobs.title")}
        </h1>
        <p className="mt-1 text-sm text-fg-subtle">{t("settings.adminSyncJobs.description")}</p>
      </div>
      <AdminSyncJobsManager />
    </div>
  );
}
