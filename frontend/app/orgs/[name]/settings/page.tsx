import { OrgProfileForm } from "@/components/orgs/org-profile-form";
import { ErrorState } from "@/components/ui/error-state";
import { errorMessage } from "@/lib/api-error-message";
import { getT } from "@/lib/i18n/server";
import { getOrg } from "@/lib/orgs";
import { authHeaders } from "@/lib/server-auth";

export const dynamic = "force-dynamic";

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
