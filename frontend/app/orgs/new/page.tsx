import Link from "next/link";
import { CreateOrgForm } from "@/components/orgs/create-org-form";
import { buttonClass } from "@/components/ui/button";
import { ErrorState } from "@/components/ui/error-state";
import { getT } from "@/lib/i18n/server";
import { getCurrentUser } from "@/lib/session";

export const dynamic = "force-dynamic";

export default async function NewOrgPage() {
  const [user, t] = await Promise.all([getCurrentUser(), getT()]);

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("org.create.title")}</h1>
        <p className="mt-1 text-sm text-fg-subtle">{t("org.create.blurb")}</p>
      </div>
      {user.ok ? (
        <CreateOrgForm loggedIn />
      ) : (
        // Whether creation is allowed at all is the server's call
        // (TF_ORG_CREATION, docs/organization-design.md §4.1) and is reported
        // as `org_creation_disabled` on submit; not being signed in is the one
        // case we can rule out before the form is even shown.
        <ErrorState
          title={t("org.create.loginRequiredTitle")}
          message={t("org.create.loginRequiredMessage")}
          action={
            <Link href="/login?next=/orgs/new" className={buttonClass({ variant: "primary" })}>
              {t("org.create.login")}
            </Link>
          }
        />
      )}
    </div>
  );
}
