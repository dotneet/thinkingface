import { SettingsNav } from "@/components/settings/settings-nav";
import { getMe } from "@/lib/auth";
import { authHeaders } from "@/lib/server-auth";

/**
 * Chrome shared by every personal /settings/* screen: a side nav so a visitor
 * can move between profile / account / tokens / SSH keys / storage / webhooks
 * / transfers / organizations / language without going back through the
 * account menu each time (mirrors OrgSettingsNav's layout, see
 * app/orgs/[name]/settings/layout.tsx). Each page still renders its own
 * `<h1>`, so this layout adds no heading of its own.
 *
 * The identity is read here rather than inside the nav so the site-admin
 * "Users" entry is there in the first paint instead of appearing a fetch
 * later and moving the list under the pointer (DESIGN.md §8). Reading it
 * needs `authHeaders()`: `credentials: "include"` is a browser-fetch concept
 * and does nothing in a Server Component (CLAUDE.md invariant 2). `apiFetch`
 * never throws, so an unreachable backend just leaves the entry out.
 */
export default async function SettingsLayout({ children }: { children: React.ReactNode }) {
  const me = await getMe({ headers: await authHeaders() });
  const isSiteAdmin = me.ok && me.data.user.is_admin;

  return (
    <div className="flex flex-col gap-6 lg:flex-row lg:items-start">
      <SettingsNav isSiteAdmin={isSiteAdmin} />
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}
