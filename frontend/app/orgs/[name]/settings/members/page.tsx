import { OrgMembersManager } from "@/components/orgs/org-members-manager";
import { getCurrentUser } from "@/lib/session";

export const dynamic = "force-dynamic";

export default async function OrgMembersSettingsPage({
  params,
}: {
  params: Promise<{ name: string }>;
}) {
  // The layout has already established that the viewer is an admin here, so
  // the only thing left to resolve is *who* they are — used to mark their own
  // row in the table.
  const [{ name }, user] = await Promise.all([params, getCurrentUser()]);
  return <OrgMembersManager org={name} viewer={user.ok ? user.data.user.username : ""} />;
}
