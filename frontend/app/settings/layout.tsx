import { SettingsNav } from "@/components/settings/settings-nav";

/**
 * Chrome shared by every personal /settings/* screen: a side nav so a visitor
 * can move between tokens / SSH keys / storage / webhooks / transfers /
 * organizations / language without going back through the account menu each
 * time (mirrors OrgSettingsNav's layout, see
 * app/orgs/[name]/settings/layout.tsx). Each page still renders its own
 * `<h1>`, so this layout adds no heading of its own.
 */
export default function SettingsLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-6 lg:flex-row lg:items-start">
      <SettingsNav />
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}
