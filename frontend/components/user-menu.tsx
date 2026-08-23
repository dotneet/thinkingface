"use client";

import {
  ArrowLeftRight,
  Building2,
  HardDrive,
  KeyRound,
  Languages,
  LogOut,
  Plus,
  Terminal,
  User as UserIcon,
  Webhook,
} from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useRef, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button, buttonClass } from "@/components/ui/button";
import { useOnClickOutside } from "@/hooks/use-on-click-outside";
import { errorMessage } from "@/lib/api-error-message";
import { logout } from "@/lib/auth";
import { useT } from "@/lib/i18n/client";
import { namespaceHref } from "@/lib/namespace";
import type { User } from "@/types/api";

export function UserMenu({
  user,
  pendingTransfersCount,
}: {
  user: User | null;
  /** Incoming pending transfers the viewer can accept/reject. Omitted (not 0) when unknown. */
  pendingTransfersCount?: number;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [loggingOut, setLoggingOut] = useState(false);
  const [logoutError, setLogoutError] = useState<string | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  const router = useRouter();
  const pathname = usePathname();
  useOnClickOutside(ref, () => setOpen(false));

  if (!user) {
    const next = pathname && pathname !== "/login" ? `?next=${encodeURIComponent(pathname)}` : "";
    return (
      <Link href={`/login${next}`} className={buttonClass({ variant: "primary" })}>
        {t("userMenu.login")}
      </Link>
    );
  }

  async function handleLogout() {
    // Keep the menu open until this resolves: closing first means a failed
    // logout leaves the user looking at a signed-in header with no hint that
    // the session is still live.
    setLogoutError(null);
    setLoggingOut(true);
    const result = await logout();
    setLoggingOut(false);
    if (!result.ok) {
      setLogoutError(errorMessage(t, result));
      return;
    }
    setOpen(false);
    router.refresh();
  }

  return (
    <div className="relative" ref={ref}>
      <Button
        onClick={() => setOpen((v) => !v)}
        className="relative h-8 w-8 rounded-full border-transparent bg-accent-muted px-0 font-semibold text-accent-strong before:absolute before:inset-[-6px] before:content-[''] hover:bg-accent-muted"
        aria-label={t("userMenu.accountMenu", { username: user.username })}
        aria-expanded={open}
        aria-haspopup="menu"
      >
        {user.username.slice(0, 1).toUpperCase()}
      </Button>
      {open && (
        <div className="absolute right-0 top-10 w-56 rounded-lg border border-border bg-bg-raised p-1 shadow-lg">
          <div className="px-3 py-2 text-sm">
            <div className="font-medium text-fg">{user.display_name || user.username}</div>
            <div className="truncate font-mono text-xs font-medium text-fg-subtle">
              {user.username}
            </div>
            <div className="truncate text-xs font-medium text-fg-subtle">{user.email}</div>
          </div>
          <div className="my-1 h-px bg-border" />
          {/* The user's own namespace page (docs/dev/namespace-design.md §8.1) —
              first, because it is the one destination that is about them
              rather than about a setting. */}
          <Link
            href={namespaceHref(user.username)}
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <UserIcon size={14} />
            {t("namespace.yourProfile")}
          </Link>
          <Link
            href="/settings/tokens"
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <KeyRound size={14} />
            {t("userMenu.accessTokens")}
          </Link>
          <Link
            href="/settings/ssh-keys"
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <Terminal size={14} />
            {t("userMenu.sshKeys")}
          </Link>
          <Link
            href="/settings/storage"
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <HardDrive size={14} />
            {t("userMenu.storageUsage")}
          </Link>
          <Link
            href="/settings/webhooks"
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <Webhook size={14} />
            {t("userMenu.webhooks")}
          </Link>
          <Link
            href="/settings/transfers"
            onClick={() => setOpen(false)}
            className="flex items-center justify-between gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <span className="flex items-center gap-2">
              <ArrowLeftRight size={14} />
              {t("userMenu.transfers")}
            </span>
            {!!pendingTransfersCount && <Badge tone="accent">{pendingTransfersCount}</Badge>}
          </Link>
          <Link
            href="/settings/organizations"
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <Building2 size={14} />
            {t("userMenu.organizations")}
          </Link>
          <Link
            href="/orgs/new"
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <Plus size={14} />
            {t("userMenu.newOrganization")}
          </Link>
          <Link
            href="/settings/language"
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <Languages size={14} />
            {t("userMenu.language")}
          </Link>
          <div className="my-1 h-px bg-border" />
          <Button
            variant="danger"
            onClick={handleLogout}
            disabled={loggingOut}
            className="w-full justify-start hover:bg-bg-hover"
          >
            <LogOut size={14} />
            {loggingOut ? t("userMenu.loggingOut") : t("userMenu.logout")}
          </Button>
          {logoutError && (
            <p role="alert" className="px-3 py-1.5 text-xs text-negative">
              {t("userMenu.logoutFailed", { message: logoutError })}
            </p>
          )}
        </div>
      )}
    </div>
  );
}
