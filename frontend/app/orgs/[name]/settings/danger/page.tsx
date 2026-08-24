import type { Metadata } from "next";
import { titleMetadata } from "@/app/page-metadata";
import { OrgDangerZone } from "@/components/orgs/org-danger-zone";
import { ErrorState } from "@/components/ui/error-state";
import { errorMessage } from "@/lib/api-error-message";
import { getT } from "@/lib/i18n/server";
import { getOrg } from "@/lib/orgs";
import { decodeRouteParams } from "@/lib/paths";
import { authHeaders } from "@/lib/server-auth";

export const dynamic = "force-dynamic";

export async function generateMetadata({
  params,
}: {
  params: Promise<{ name: string }>;
}): Promise<Metadata> {
  const [{ name }, t] = await Promise.all([params.then(decodeRouteParams), getT()]);
  return titleMetadata(name, t("meta.settings"), t("meta.dangerZone"));
}

export default async function OrgDangerSettingsPage({
  params,
}: {
  params: Promise<{ name: string }>;
}) {
  const [{ name }, t] = await Promise.all([params, getT()]);
  // Re-read for `num_repos`: the layout's copy checked the role and cannot be
  // passed down.
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

  return <OrgDangerZone org={result.data.org} />;
}
