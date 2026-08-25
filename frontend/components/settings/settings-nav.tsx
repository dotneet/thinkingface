"use client";

import type { LucideIcon } from "lucide-react";
import {
  ArrowLeftRight,
  Building2,
  Fingerprint,
  Gauge,
  Globe,
  HardDrive,
  KeyRound,
  Lock,
  RefreshCw,
  User,
  Users,
  Webhook,
} from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/cn";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";

type NavItem = { segment: string; labelKey: MessageKey; icon: LucideIcon };

const ITEMS: NavItem[] = [
  { segment: "/profile", labelKey: "settings.nav.profile", icon: User },
  // "Account" is the credential (the password); "Profile" is how the account
  // is presented. Adjacent because a visitor looking for one often means the
  // other.
  { segment: "/account", labelKey: "settings.account.navLabel", icon: Lock },
  { segment: "/tokens", labelKey: "settings.nav.tokens", icon: KeyRound },
  { segment: "/ssh-keys", labelKey: "settings.nav.sshKeys", icon: Fingerprint },
  { segment: "/storage", labelKey: "settings.nav.storage", icon: HardDrive },
  { segment: "/webhooks", labelKey: "settings.nav.webhooks", icon: Webhook },
  { segment: "/transfers", labelKey: "settings.nav.transfers", icon: ArrowLeftRight },
  { segment: "/organizations", labelKey: "settings.nav.organizations", icon: Building2 },
  { segment: "/language", labelKey: "settings.nav.language", icon: Globe },
];

/**
 * Instance-wide administration, shown only to accounts carrying
 * `users.is_admin`. Kept out of ITEMS and appended last so that whether it is
 * there or not, nothing above it moves.
 */
const ADMIN_ITEMS: NavItem[] = [
  { segment: "/admin/users", labelKey: "settings.adminUsers.navLabel", icon: Users },
  { segment: "/admin/sync-jobs", labelKey: "settings.adminSyncJobs.navLabel", icon: RefreshCw },
  { segment: "/admin/namespaces", labelKey: "settings.adminQuotas.navLabel", icon: Gauge },
];

/**
 * Side navigation shared by every /settings/* screen (mirrors OrgSettingsNav).
 *
 * `isSiteAdmin` is resolved by the layout, on the server, rather than fetched
 * here: an entry that appears one render later would move the rest of the
 * list under the pointer (DESIGN.md §8). It only decides what is *shown* —
 * every /admin route is enforced by the backend, which answers 403 to anyone
 * without the flag.
 */
export function SettingsNav({ isSiteAdmin = false }: { isSiteAdmin?: boolean }) {
  const t = useT();
  const pathname = usePathname();
  const items = isSiteAdmin ? [...ITEMS, ...ADMIN_ITEMS] : ITEMS;

  return (
    <nav className="flex w-full shrink-0 flex-row gap-1 overflow-x-auto lg:w-56 lg:flex-col">
      {items.map((item) => {
        const href = `/settings${item.segment}`;
        const active = pathname === href;
        return (
          <Link
            key={item.segment}
            href={href}
            aria-current={active ? "page" : undefined}
            className={cn(
              "flex shrink-0 items-center gap-2 rounded-md px-3 py-1.5 text-sm transition-colors",
              active
                ? "bg-accent-muted font-medium text-accent-strong"
                : "text-fg-muted hover:bg-bg-hover hover:text-fg",
            )}
          >
            <item.icon size={14} />
            {t(item.labelKey)}
          </Link>
        );
      })}
    </nav>
  );
}
