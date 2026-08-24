import type { Metadata } from "next";
import { titleMetadata } from "@/app/page-metadata";
import { OrgProfileForm } from "@/components/orgs/org-profile-form";
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
  return titleMetadata(name, t("meta.settings"), t("meta.profile"));
}

export default async function OrgProfileSettingsPage({
  params,
}: {
  params: Promise<{ name: string }>;
}) {
  const [{ name }, t] = await Promise.all([params, getT()]);
  // The layout already fetched this to check the admin role, but a layout
  // cannot hand data to its children; this second read is what fills the form.
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

  return <OrgProfileForm org={result.data.org} />;
}
