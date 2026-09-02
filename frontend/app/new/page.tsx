import type { Metadata } from "next";
import Link from "next/link";
import { titleMetadata } from "@/app/page-metadata";
import { CreateRepoForm } from "@/components/repo/create-repo-form";
import { ErrorState } from "@/components/ui/error-state";
import { isUnauthorized } from "@/lib/api";
import { getT } from "@/lib/i18n/server";
import { writableNamespaces } from "@/lib/namespace";
import { getCurrentUser } from "@/lib/session";

export const dynamic = "force-dynamic";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getT();
  return titleMetadata(t("meta.newRepository"));
}

export default async function NewRepoPage({
  searchParams,
}: {
  /** `?ns=` preselects a namespace — where the "create the first repository"
      call to action on an empty namespace page points. */
  searchParams: Promise<{ ns?: string }>;
}) {
  const [user, t, sp] = await Promise.all([getCurrentUser(), getT(), searchParams]);
  // The whole Namespace (name + kind + role), not just the name: the form
  // badges organisations and reads their creation policy. Filtered to write
  // or admin -- `/api/v1/me` also lists namespaces the user can only read,
  // which the backend rejects a repository creation into (lib/namespace.ts).
  const namespaces = user.ok ? writableNamespaces(user.data.user) : [];
  // `user.ok` is false for a 500 and for an unreachable backend just as it is
  // for a signed-out visitor, and the three are not the same thing
  // (DESIGN.md §9): only a 401 means "log in". Anything else used to render
  // the login notice above an empty namespace picker, telling a signed-in
  // user they were signed out.
  const signedOut = !user.ok && isUnauthorized(user);
  const loadFailed = !user.ok && !signedOut;

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("newRepo.title")}</h1>
        <p className="mt-1 text-sm text-fg-subtle">{t("newRepo.blurb")}</p>
      </div>
      {signedOut && (
        <p className="rounded-md border border-border bg-bg-sunken px-3 py-2 text-sm text-fg-subtle">
          {t("newRepo.loginNotice.prefix")}
          <Link href="/login?next=/new" className="text-accent hover:underline">
            {t("newRepo.loginNotice.link")}
          </Link>
          {t("newRepo.loginNotice.suffix")}
        </p>
      )}
      {loadFailed ? (
        <ErrorState
          title={t("ui.errorStateTitle")}
          message={t("newRepo.accountLoadFailed")}
          hint={t("newRepo.accountLoadFailedHint")}
        />
      ) : (
        <CreateRepoForm namespaces={namespaces} loggedIn={user.ok} initialNamespace={sp.ns} />
      )}
    </div>
  );
}
