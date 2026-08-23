"use client";

import type { LucideIcon } from "lucide-react";
import {
  ArrowLeftRight,
  Building2,
  Fingerprint,
  Globe,
  HardDrive,
  KeyRound,
  User,
  Webhook,
} from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/cn";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";

const ITEMS: { segment: string; labelKey: MessageKey; icon: LucideIcon }[] = [
  { segment: "/profile", labelKey: "settings.nav.profile", icon: User },
  { segment: "/tokens", labelKey: "settings.nav.tokens", icon: KeyRound },
  { segment: "/ssh-keys", labelKey: "settings.nav.sshKeys", icon: Fingerprint },
  { segment: "/storage", labelKey: "settings.nav.storage", icon: HardDrive },
  { segment: "/webhooks", labelKey: "settings.nav.webhooks", icon: Webhook },
  { segment: "/transfers", labelKey: "settings.nav.transfers", icon: ArrowLeftRight },
  { segment: "/organizations", labelKey: "settings.nav.organizations", icon: Building2 },
  { segment: "/language", labelKey: "settings.nav.language", icon: Globe },
];

/** Side navigation shared by every /settings/* screen (mirrors OrgSettingsNav). */
export function SettingsNav() {
  const t = useT();
  const pathname = usePathname();

  return (
    <nav className="flex w-full shrink-0 flex-row gap-1 overflow-x-auto lg:w-56 lg:flex-col">
      {ITEMS.map((item) => {
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
