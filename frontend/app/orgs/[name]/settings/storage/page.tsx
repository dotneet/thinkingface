import type { Metadata } from "next";

import { titleMetadata } from "@/app/page-metadata";
import { StorageUsage } from "@/components/settings/storage-usage";
import { ErrorState } from "@/components/ui/error-state";
import { errorMessage } from "@/lib/api-error-message";
import { getT } from "@/lib/i18n/server";
import { getOrg, orgSettingsHref } from "@/lib/orgs";
import { decodeRouteParams } from "@/lib/paths";
import { authHeaders } from "@/lib/server-auth";

export const dynamic = "force-dynamic";

export async function generateMetadata({
  params,
}: {
  params: Promise<{ name: string }>;
}): Promise<Metadata> {
  const [{ name }, t] = await Promise.all([params.then(decodeRouteParams), getT()]);
  return titleMetadata(name, t("meta.settings"), t("meta.storage"));
}

export default async function OrgStorageSettingsPage({
  params,
}: {
  params: Promise<{ name: string }>;
}) {
  // Decoded like every other page boundary (and like this file's own
  // `generateMetadata`), so `StorageUsage` and the login-return href get the
  // real organisation name rather than its percent-encoded URL segment.
  const [{ name }, t] = await Promise.all([params.then(decodeRouteParams), getT()]);
  // Re-read for `num_repos`: the layout's copy checked the role and cannot be
  // passed down (see danger/page.tsx's identical comment). StorageUsage needs
  // it to tell a genuinely empty organisation apart from one whose usage this
  // viewer simply isn't a member of.
  const result = await getOrg(name, { headers: await authHeaders() });

  if (!result.ok) {
    return (
      <ErrorState
        title={t("org.page.loadFailedTitle")}
        message={errorMessage(t, result)}
        hint={t("org.settings.loadFailedHint")}
      />
    );
  }

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h2 className="text-sm font-semibold text-fg">{t("org.settings.storage.title")}</h2>
        <p className="mt-1 text-sm text-fg-subtle">{t("org.settings.storage.description")}</p>
      </div>
      <StorageUsage
        namespace={name}
        namespaceRepoCount={result.data.org.num_repos}
        loginNext={`${orgSettingsHref(name)}/storage`}
        emptyTitle={t("org.settings.storage.emptyTitle")}
        emptyDescription={t("org.settings.storage.emptyDescription")}
      />
    </div>
  );
}
