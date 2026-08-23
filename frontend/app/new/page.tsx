import Link from "next/link";
import { CreateRepoForm } from "@/components/repo/create-repo-form";
import { getT } from "@/lib/i18n/server";
import { getCurrentUser } from "@/lib/session";

export const dynamic = "force-dynamic";

export default async function NewRepoPage({
  searchParams,
}: {
  /** `?ns=` preselects a namespace — where the "create the first repository"
      call to action on an empty namespace page points. */
  searchParams: Promise<{ ns?: string }>;
}) {
  const [user, t, sp] = await Promise.all([getCurrentUser(), getT(), searchParams]);
  // The whole Namespace (name + kind + role), not just the name: the form
  // badges organisations and reads their creation policy.
  const namespaces = user.ok ? user.data.user.namespaces : [];

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("newRepo.title")}</h1>
        <p className="mt-1 text-sm text-fg-subtle">{t("newRepo.blurb")}</p>
      </div>
      {!user.ok && (
        <p className="rounded-md border border-border bg-bg-sunken px-3 py-2 text-sm text-fg-subtle">
          {t("newRepo.loginNotice.prefix")}
          <Link href="/login?next=/new" className="text-accent hover:underline">
            {t("newRepo.loginNotice.link")}
          </Link>
          {t("newRepo.loginNotice.suffix")}
        </p>
      )}
      <CreateRepoForm namespaces={namespaces} loggedIn={user.ok} initialNamespace={sp.ns} />
    </div>
  );
}
