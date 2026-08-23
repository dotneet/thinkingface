"use client";

import type { LucideIcon } from "lucide-react";
import {
  AlertTriangle,
  HardDrive,
  ScrollText,
  SlidersHorizontal,
  Users,
  Webhook,
} from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/cn";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import { orgSettingsHref } from "@/lib/orgs";

const ITEMS: { segment: string; labelKey: MessageKey; icon: LucideIcon }[] = [
  { segment: "", labelKey: "org.settings.navProfile", icon: SlidersHorizontal },
  { segment: "/members", labelKey: "org.settings.navMembers", icon: Users },
  { segment: "/webhooks", labelKey: "org.settings.navWebhooks", icon: Webhook },
  { segment: "/storage", labelKey: "org.settings.navStorage", icon: HardDrive },
  { segment: "/audit-log", labelKey: "org.settings.navAuditLog", icon: ScrollText },
  { segment: "/danger", labelKey: "org.settings.navDanger", icon: AlertTriangle },
];

/** Side navigation shared by every /orgs/{name}/settings screen. */
export function OrgSettingsNav({ name }: { name: string }) {
  const t = useT();
  const pathname = usePathname();
  const base = orgSettingsHref(name);

  return (
    <nav className="flex w-full shrink-0 flex-row gap-1 overflow-x-auto lg:w-56 lg:flex-col">
      {ITEMS.map((item) => {
        const href = `${base}${item.segment}`;
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
